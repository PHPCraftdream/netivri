package proxyclient

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
)

func socks5Handshake(conn net.Conn, p Proxy, targetHost string, targetPort int) error {
	methods := []byte{0}
	if p.Username != "" || p.Password != "" {
		methods = append(methods, 2)
	}
	greeting := []byte{5, byte(len(methods))}
	greeting = append(greeting, methods...)
	if err := writeAll(conn, greeting); err != nil {
		return proxyError(StageHandshake, 0, err)
	}
	var choice [2]byte
	if _, err := io.ReadFull(conn, choice[:]); err != nil {
		return proxyError(StageHandshake, 0, err)
	}
	if choice[0] != 5 {
		return proxyError(StageHandshake, 0, errInvalidProxy)
	}
	switch choice[1] {
	case 0:
	case 2:
		if len(methods) != 2 {
			return proxyError(StageHandshake, 0, errInvalidProxy)
		}
		err := socks5Auth(conn, p.Username, p.Password)
		if err != nil {
			return err
		}
	case 0xff:
		return proxyError(StageRefused, 0, errInvalidProxy)
	default:
		return proxyError(StageHandshake, 0, errInvalidProxy)
	}

	request, err := socks5Request(targetHost, targetPort)
	if err != nil {
		return proxyError(StageOther, 0, err)
	}
	if err := writeAll(conn, request); err != nil {
		return targetSentError(StageHandshake, 0, err)
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return targetSentError(StageHandshake, 0, err)
	}
	if header[0] != 5 || header[2] != 0 {
		return targetSentError(StageHandshake, 0, errInvalidProxy)
	}
	var addrLen int
	switch header[3] {
	case 1:
		addrLen = 4
	case 4:
		addrLen = 16
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return targetSentError(StageHandshake, 0, err)
		}
		if n[0] == 0 {
			return targetSentError(StageHandshake, 0, errInvalidProxy)
		}
		addrLen = int(n[0])
	default:
		return targetSentError(StageHandshake, 0, errInvalidProxy)
	}
	var tail [2]byte
	if _, err := io.CopyN(io.Discard, conn, int64(addrLen)); err != nil {
		return targetSentError(StageHandshake, 0, err)
	}
	if _, err := io.ReadFull(conn, tail[:]); err != nil {
		return targetSentError(StageHandshake, 0, err)
	}
	if header[1] != 0 {
		stage := StageOther
		switch header[1] {
		case 2:
			stage = StageRefused
		case 3, 4, 5, 6:
			stage = StageTarget
		}
		return targetSentError(stage, int(header[1]), errInvalidProxy)
	}
	return nil
}

func socks5Auth(conn net.Conn, username, password string) error {
	if len(username) > 255 || len(password) > 255 {
		return proxyError(StageOther, 0, errInvalidProxy)
	}
	message := []byte{1, byte(len(username))}
	message = append(message, username...)
	message = append(message, byte(len(password)))
	message = append(message, password...)
	if err := writeAll(conn, message); err != nil {
		return proxyError(StageHandshake, 0, err)
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return proxyError(StageHandshake, 0, err)
	}
	if response[0] != 1 {
		return proxyError(StageHandshake, 0, errInvalidProxy)
	}
	if response[1] != 0 {
		return proxyError(StageRefused, 0, errInvalidProxy)
	}
	return nil
}

func socks5Request(host string, port int) ([]byte, error) {
	request := []byte{5, 1, 0}
	if strings.Contains(host, ":") {
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, errInvalidTarget
		}
		request = append(request, 4)
		request = append(request, ip.To16()...)
	} else if ip := net.ParseIP(host); ip != nil {
		request = append(request, 1)
		request = append(request, ip.To4()...)
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errInvalidTarget
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(port))
	request = append(request, b[:]...)
	return request, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
