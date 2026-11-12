package proxyclient

import (
	"bufio"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"sync"
)

const maxHTTPHeaders = 16 * 1024

func httpConnectHandshake(conn net.Conn, p Proxy, authority string) (net.Conn, error) {
	var request strings.Builder
	request.WriteString("CONNECT ")
	request.WriteString(authority)
	request.WriteString(" HTTP/1.1\r\nHost: ")
	request.WriteString(authority)
	request.WriteString("\r\n")
	if p.Username != "" || p.Password != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + p.Password))
		request.WriteString("Proxy-Authorization: Basic ")
		request.WriteString(credentials)
		request.WriteString("\r\n")
	}
	request.WriteString("\r\n")
	if err := writeAll(conn, []byte(request.String())); err != nil {
		return nil, targetSentError(StageHandshake, 0, err)
	}

	reader := bufio.NewReaderSize(conn, maxHTTPHeaders)
	used := 0
	line, err := readHTTPLine(reader, &used)
	if err != nil {
		return nil, targetSentError(StageHandshake, 0, err)
	}
	status, ok := parseHTTPStatus(line)
	if !ok {
		return nil, targetSentError(StageHandshake, 0, errInvalidProxy)
	}
	for {
		line, err = readHTTPLine(reader, &used)
		if err != nil {
			return nil, targetSentError(StageHandshake, 0, err)
		}
		if line == "\r\n" {
			break
		}
		if !validHTTPHeader(line) {
			return nil, targetSentError(StageHandshake, 0, errInvalidProxy)
		}
	}
	if status < 200 || status > 299 {
		stage := StageOther
		switch status {
		case 403, 407:
			stage = StageRefused
		case 502, 503, 504:
			stage = StageTarget
		}
		return nil, targetSentError(stage, status, errInvalidProxy)
	}
	buffered := reader.Buffered()
	if buffered == 0 {
		return conn, nil
	}
	prefix, err := reader.Peek(buffered)
	if err != nil {
		return nil, targetSentError(StageHandshake, 0, err)
	}
	return &bufferedConn{Conn: conn, prefix: append([]byte(nil), prefix...)}, nil
}

func readHTTPLine(r *bufio.Reader, used *int) (string, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		*used += len(part)
		if *used > maxHTTPHeaders {
			return "", errInvalidProxy
		}
		line = append(line, part...)
		if err == nil {
			if len(line) < 2 || line[len(line)-2] != '\r' {
				return "", errInvalidProxy
			}
			return string(line), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
	}
}

func parseHTTPStatus(line string) (int, bool) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	// The reason phrase may be absent, and then so may the space before it.
	if len(line) < 12 || (line[:8] != "HTTP/1.0" && line[:8] != "HTTP/1.1") || line[8] != ' ' || len(line) > 12 && line[12] != ' ' {
		return 0, false
	}
	if line[9] < '0' || line[9] > '9' || line[10] < '0' || line[10] > '9' || line[11] < '0' || line[11] > '9' {
		return 0, false
	}
	for _, c := range line[12:] {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return 0, false
		}
	}
	status := int(line[9]-'0')*100 + int(line[10]-'0')*10 + int(line[11]-'0')
	if status < 100 || status > 599 {
		return 0, false
	}
	return status, true
}

func validHTTPHeader(line string) bool {
	if len(line) < 3 || line[len(line)-2] != '\r' || line[len(line)-1] != '\n' || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	for _, c := range line[:colon] {
		if !isHTTPToken(c) {
			return false
		}
	}
	for _, c := range line[colon+1 : len(line)-2] {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

func isHTTPToken(c rune) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", c)
}

type bufferedConn struct {
	net.Conn
	mu     sync.Mutex
	prefix []byte
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		if len(c.prefix) == 0 {
			c.prefix = nil
		}
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}
