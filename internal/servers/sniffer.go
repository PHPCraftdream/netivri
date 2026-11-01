package servers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// MaxClientHelloBytes is the maximum accepted TLS ClientHello size.
const MaxClientHelloBytes = 64 << 10

// ErrClientHelloIncomplete indicates that more TLS record data is needed.
var ErrClientHelloIncomplete = errors.New("incomplete TLS ClientHello")

// ClientHello contains routing information parsed from a TLS ClientHello.
type ClientHello struct {
	SNI  string
	ALPN []string
}

// Sniffer matches routing keys against parsed TLS ClientHello values.
type Sniffer interface {
	MatchKeys(map[string]string) ([]string, []string, bool)
}

// TLSMatchKeys returns protocol-declared routing keys and capability.
func TLSMatchKeys(spec Spec) (sni, alpn []string, explicit, capable bool) {
	p, ok := Lookup(spec.Protocol)
	if !ok {
		return nil, nil, false, false
	}
	m, ok := p.(Sniffer)
	if !ok {
		return nil, nil, false, false
	}
	sni, alpn, explicit = m.MatchKeys(spec.Params)
	return sni, alpn, explicit, true
}

func MatchClientHello(spec Spec, hello ClientHello) bool {
	sni, alpn, _, capable := TLSMatchKeys(spec)
	if !capable || !matchesSNI(sni, hello.SNI) {
		return false
	}
	return matchesALPN(alpn, hello.ALPN)
}

func matchesSNI(keys []string, value string) bool {
	if len(keys) == 0 {
		return true
	}
	for _, key := range keys {
		if strings.EqualFold(key, value) {
			return true
		}
	}
	return false
}
func matchesALPN(keys, values []string) bool {
	if len(keys) == 0 {
		return true
	}
	for _, key := range keys {
		for _, value := range values {
			if key == value {
				return true
			}
		}
	}
	return false
}

func SnifferAmbiguous(aSNI, aALPN, bSNI, bALPN []string) bool {
	if !overlapSNI(aSNI, bSNI) || !overlapALPN(aALPN, bALPN) {
		return false
	}
	return true
}
func overlapSNI(a, b []string) bool {
	if a == nil || b == nil {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}
func overlapALPN(a, b []string) bool {
	if a == nil || b == nil {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func ValidateSharedPort(specs []Spec) error {
	if len(specs) == 0 {
		return nil
	}
	trialMembers, vmessUsers := 0, 0
	anyTrial := false
	for _, spec := range specs {
		tls, trial := ShareMode(spec)
		if !tls && !trial && len(specs) > 1 {
			return &cli.Error{Code: "PORT_IN_USE", Message: "server protocol cannot share a port", Exit: cli.ExitConflict}
		}
		if trial {
			anyTrial = true
			if spec.Default {
				return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "trial-authenticated servers cannot be default", Exit: cli.ExitConflict, Hint: "choose a TLS server as the default"}
			}
			trialMembers++
			if spec.Protocol == "vmess" {
				vmessUsers += len(spec.Accounts)
			}
		}
	}
	if trialMembers > 16 || vmessUsers > 1024 {
		return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "shared endpoint exceeds user limit", Exit: cli.ExitConflict}
	}
	if len(specs) == 1 {
		return nil
	}
	address := normalizeListenAddress(specs[0])
	for _, spec := range specs[1:] {
		if normalizeListenAddress(spec) != address {
			return &cli.Error{Code: "PORT_IN_USE", Message: "shared servers must use the same listen address", Exit: cli.ExitConflict}
		}
	}
	if anyTrial && trialSecretsPresent(specs) {
		if err := ValidateTrialSecrets(specs); err != nil {
			return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "shared endpoint has invalid or ambiguous trial credentials", Exit: cli.ExitConflict, Hint: "use distinct valid trial credentials"}
		}
	}
	keys := make([]struct {
		sni, alpn         []string
		explicit, capable bool
	}, len(specs))
	defaults := 0
	for i, spec := range specs {
		sni, alpn, explicit, capable := TLSMatchKeys(spec)
		keys[i] = struct {
			sni, alpn         []string
			explicit, capable bool
		}{sni, alpn, explicit, capable}
		if capable && spec.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "only one server may be the default", Exit: cli.ExitConflict, Hint: "give one of them another sni"}
	}
	for i, spec := range specs {
		if keys[i].capable && !spec.Default && !keys[i].explicit {
			return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "shared server needs explicit SNI", Exit: cli.ExitConflict, Hint: "set sni on " + spec.Name + " or mark it as the default"}
		}
	}
	for i := range specs {
		if !keys[i].capable {
			continue
		}
		for j := i + 1; j < len(specs); j++ {
			if keys[j].capable && SnifferAmbiguous(keys[i].sni, keys[i].alpn, keys[j].sni, keys[j].alpn) {
				return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "shared server TLS match keys overlap", Exit: cli.ExitConflict, Hint: "give one of them another sni"}
			}
		}
	}
	return nil
}
func trialSecretsPresent(specs []Spec) bool {
	for _, spec := range specs {
		_, trial := ShareMode(spec)
		if trial {
			for _, login := range spec.Accounts {
				if len(spec.Users[login]) > 0 {
					return true
				}
			}
		}
	}
	return false
}

func normalizeListenAddress(s Spec) string {
	bind := s.Bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	ip := net.ParseIP(bind)
	if ip != nil {
		bind = ip.String()
	}
	return net.JoinHostPort(strings.ToLower(bind), fmt.Sprint(s.Port))
}

func ParseClientHello(data []byte) (ClientHello, error) {
	var result ClientHello
	if len(data) > MaxClientHelloBytes {
		return result, errors.New("TLS ClientHello exceeds limit")
	}
	var handshake []byte
	for offset := 0; ; {
		if len(handshake) >= 4 {
			hlen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if hlen > MaxClientHelloBytes {
				return result, errors.New("TLS ClientHello exceeds limit")
			}
			if len(handshake) >= 4+hlen {
				break
			}
		}
		if offset == len(data) {
			return result, ErrClientHelloIncomplete
		}
		if len(data)-offset < 5 {
			return result, ErrClientHelloIncomplete
		}
		if data[offset] != 22 || data[offset+1] != 3 {
			return result, errors.New("invalid TLS record")
		}
		n := int(binary.BigEndian.Uint16(data[offset+3 : offset+5]))
		if n == 0 {
			return result, errors.New("invalid TLS record length")
		}
		if len(data)-offset-5 < n {
			return result, ErrClientHelloIncomplete
		}
		handshake = append(handshake, data[offset+5:offset+5+n]...)
		offset += 5 + n
		if offset > MaxClientHelloBytes {
			return result, errors.New("TLS ClientHello exceeds limit")
		}
	}
	if len(handshake) < 4 {
		return result, ErrClientHelloIncomplete
	}
	if handshake[0] != 1 {
		return result, errors.New("not a TLS ClientHello")
	}
	hlen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	if hlen > MaxClientHelloBytes {
		return result, errors.New("TLS ClientHello exceeds limit")
	}
	if len(handshake)-4 < hlen {
		return result, ErrClientHelloIncomplete
	}
	b := handshake[4 : 4+hlen]
	if len(b) < 2+32+1 {
		return result, errors.New("malformed TLS ClientHello")
	}
	o := 34
	sid := int(b[o])
	o++
	if o+sid+2 > len(b) {
		return result, errors.New("malformed TLS ClientHello")
	}
	o += sid
	cs := int(binary.BigEndian.Uint16(b[o : o+2]))
	o += 2
	if cs == 0 || cs%2 != 0 || o+cs+1 > len(b) {
		return result, errors.New("malformed TLS ClientHello")
	}
	o += cs
	comp := int(b[o])
	o++
	if comp == 0 || o+comp > len(b) {
		return result, errors.New("malformed TLS ClientHello")
	}
	o += comp
	if o == len(b) {
		return result, nil
	}
	if o+2 > len(b) {
		return result, errors.New("malformed TLS ClientHello")
	}
	extLen := int(binary.BigEndian.Uint16(b[o : o+2]))
	o += 2
	if o+extLen != len(b) {
		return result, errors.New("malformed TLS extensions")
	}
	end := o + extLen
	seenExtensions := make(map[uint16]bool)
	for o < end {
		if o+4 > end {
			return result, errors.New("malformed TLS extension")
		}
		typ, n := binary.BigEndian.Uint16(b[o:o+2]), int(binary.BigEndian.Uint16(b[o+2:o+4]))
		o += 4
		if o+n > end {
			return result, errors.New("malformed TLS extension")
		}
		e := b[o : o+n]
		o += n
		if seenExtensions[typ] {
			return result, errors.New("duplicate TLS extension")
		}
		seenExtensions[typ] = true
		switch typ {
		case 0:
			if len(e) < 2 || int(binary.BigEndian.Uint16(e[:2])) != len(e)-2 {
				return result, errors.New("malformed SNI extension")
			}
			p := 2
			for p < len(e) {
				if p+3 > len(e) {
					return result, errors.New("malformed SNI name")
				}
				kind := e[p]
				l := int(binary.BigEndian.Uint16(e[p+1 : p+3]))
				p += 3
				if p+l > len(e) {
					return result, errors.New("malformed SNI name")
				}
				if kind == 0 {
					name := string(e[p : p+l])
					if l == 0 || strings.ContainsAny(name, "\x00\r\n") || result.SNI != "" {
						return result, errors.New("invalid or duplicate SNI hostname")
					}
					result.SNI = strings.ToLower(name)
				}
				p += l
			}
		case 16:
			if len(e) < 2 || int(binary.BigEndian.Uint16(e[:2])) != len(e)-2 {
				return result, errors.New("malformed ALPN extension")
			}
			p := 2
			for p < len(e) {
				l := int(e[p])
				p++
				if l == 0 || p+l > len(e) {
					return result, errors.New("malformed ALPN protocol")
				}
				result.ALPN = append(result.ALPN, string(e[p:p+l]))
				p += l
			}
		}
	}
	return result, nil
}
