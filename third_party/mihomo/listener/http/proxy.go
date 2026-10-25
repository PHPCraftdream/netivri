// Modified by the netivri authors: proxy authentication receives the client address (VerifyFrom).

package http

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"

	"github.com/metacubex/http"
)

type bodyWrapper struct {
	io.ReadCloser
	once     sync.Once
	onHitEOF func()
}

func (b *bodyWrapper) Read(p []byte) (n int, err error) {
	n, err = b.ReadCloser.Read(p)
	if err == io.EOF && b.onHitEOF != nil {
		b.once.Do(b.onHitEOF)
	}
	return n, err
}

func HandleConn(c net.Conn, tunnel C.Tunnel, store auth.AuthStore, additions ...inbound.Addition) {
	additions = append(additions, inbound.Placeholder) // Add a placeholder for InUser
	inUserIdx := len(additions) - 1
	client := newClient(c, tunnel, additions)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peekMutex := sync.Mutex{}

	conn := N.NewBufferedConn(c)

	authenticator := store.Authenticator()
	trusted := authenticator == nil // disable authenticate if lru is nil
	// Verified by the netivri authors: SOCKS5 negotiates user/password once per
	// connection (transport/socks5 ServerHandshake), so there is no per-request
	// account switch on that path; HTTP re-evaluates every request.
	lastUser := ""

	for {
		peekMutex.Lock()
		request, err := ReadRequest(conn.Reader())
		peekMutex.Unlock()
		if err != nil {
			break
		}

		request.RemoteAddr = conn.RemoteAddr().String()

		keepAlive := strings.TrimSpace(strings.ToLower(request.Header.Get("Proxy-Connection"))) == "keep-alive"

		resp, user := authenticate(request, authenticator, conn.RemoteAddr()) // always call authenticate function to get user
		if resp == nil {
			trusted = true
		} else {
			// Modified by the netivri authors: a failed re-authentication on a
			// keep-alive connection revokes the trust of earlier requests and
			// the unauthenticated user must not reach the tunnel.
			//
			// 403 is the stock answer for a request that CARRIES credentials
			// but fails verification (wrong password or VerifyFrom refusal);
			// 407 is answered only when no credentials were sent, so a
			// disallowed address is indistinguishable from a wrong password.
			trusted = false
			user = ""
		}
		additions[inUserIdx] = inbound.WithInUser(user)

		if trusted {
			if request.Method == http.MethodConnect {
				// Manual writing to support CONNECT for http 1.0 (workaround for uplay client)
				if _, err = fmt.Fprintf(conn, "HTTP/%d.%d %03d %s\r\n\r\n", request.ProtoMajor, request.ProtoMinor, http.StatusOK, "Connection established"); err != nil {
					break // close connection
				}

				tunnel.HandleTCPConn(inbound.NewHTTPS(request, conn, additions...))

				return // hijack connection
			}

			host := request.Header.Get("Host")
			if host != "" {
				request.Host = host
			}

			request.RequestURI = ""

			if isUpgradeRequest(request) {
				handleUpgrade(conn, request, tunnel, additions...)

				return // hijack connection
			}

			// ensure there is a client with correct additions
			// when the authenticated user changed, outbound client should close idle connections
			if user != lastUser {
				client.CloseIdleConnections()
				lastUser = user
			}

			removeHopByHopHeaders(request.Header)
			removeExtraHTTPHostPort(request)

			if request.URL.Scheme == "" || request.URL.Host == "" {
				resp = responseWith(request, http.StatusBadRequest)
			} else {
				request = request.WithContext(ctx)

				startBackgroundRead := func() {
					go func() {
						peekMutex.Lock()
						defer peekMutex.Unlock()
						_, err := conn.Peek(1)
						if err != nil {
							cancel()
						}
					}()
				}
				if request.Body == nil || request.Body == http.NoBody {
					startBackgroundRead()
				} else {
					request.Body = &bodyWrapper{ReadCloser: request.Body, onHitEOF: startBackgroundRead}
				}
				resp, err = client.Do(request)
				if err != nil {
					resp = responseWith(request, http.StatusBadGateway)
				}
			}

			removeHopByHopHeaders(resp.Header)
		}

		if !keepAlive {
			resp.Close = true // close connection if keep-alive is not set
		}
		if keepAlive && resp.ContentLength > 0 {
			resp.Close = false // don't need to close connection if content length is positive numbers
		}

		if !resp.Close {
			resp.Header.Set("Proxy-Connection", "keep-alive")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Set("Keep-Alive", "timeout=4")
		}

		err = resp.Write(conn)
		if err != nil || resp.Close {
			break // close connection
		}
	}

	_ = conn.Close()
}

func authenticate(request *http.Request, authenticator auth.Authenticator, remote net.Addr) (resp *http.Response, user string) {
	credential := parseBasicProxyAuthorization(request)
	if credential == "" && authenticator != nil {
		resp = responseWith(request, http.StatusProxyAuthRequired)
		resp.Header.Set("Proxy-Authenticate", "Basic")
		return
	}
	user, pass, err := decodeBasicProxyAuthorization(credential)
	authed := authenticator == nil || (err == nil && auth.VerifyWithRemote(authenticator, user, pass, remote))
	if !authed {
		log.Infoln("Auth failed from %s", request.RemoteAddr)
		return responseWith(request, http.StatusForbidden), user
	}
	log.Debugln("Auth success from %s -> %s", request.RemoteAddr, user)
	return
}

func responseWith(request *http.Request, statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Proto:      request.Proto,
		ProtoMajor: request.ProtoMajor,
		ProtoMinor: request.ProtoMinor,
		Header:     http.Header{},
	}
}
