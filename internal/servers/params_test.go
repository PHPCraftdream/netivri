package servers_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func auditedProtocols(t *testing.T) []servers.Protocol {
	t.Helper()
	registered := servers.Registered()
	registeredNames := make([]string, 0, len(registered))
	for _, protocol := range registered {
		registeredNames = append(registeredNames, protocol.Name())
	}
	allowedTestOnly := map[string]bool{
		"a-test": true, "z-test": true,
		"sniffer-custom-matchkeys-test": true,
		"store-test":                    true,
	}
	unexpected := make([]string, 0)
	for _, name := range registeredNames {
		if _, audited := auditedMetadata()[name]; !audited && !allowedTestOnly[name] {
			unexpected = append(unexpected, name)
		}
	}
	for name := range auditedMetadata() {
		if !containsString(registeredNames, name) {
			unexpected = append(unexpected, "missing:"+name)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) != 0 {
		t.Fatalf("registry differs from audited protocols; registered=%v unexpected=%v", registeredNames, unexpected)
	}
	protocols := make([]servers.Protocol, 0, len(auditedMetadata()))
	for _, protocol := range registered {
		if _, audited := auditedMetadata()[protocol.Name()]; audited {
			protocols = append(protocols, protocol)
		}
	}
	return protocols
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func auditedMetadata() map[string][]string {
	return map[string][]string{
		"anytls":      {"sni", "padding_scheme", "cert_file", "key_file"},
		"hysteria2":   {"sni", "cert_file", "key_file", "obfs", "up", "down"},
		"mieru":       {"transport", "multiplexing"},
		"shadowquic":  {"sni", "cert_file", "key_file", "alpn", "quic_versions", "max_idle_time", "max_datagram_frame_size", "recv_window_conn", "recv_window", "disable_mtu_discovery", "congestion_controller"},
		"shadowsocks": {"cipher", "udp"}, "snell": {"version", "obfs", "obfs_host"},
		"sudoku":        {"table-type", "padding-min", "padding-max", "enable-pure-downlink", "disable-http-mask", "http-mask-mode"},
		"trojan":        {"sni", "alpn", "cert_file", "key_file"},
		"trusttunnel":   {"sni", "alpn", "cert_file", "key_file", "network", "udp", "quic"},
		"tuic":          {"sni", "alpn", "cert_file", "key_file", "congestion", "udp_relay_mode"},
		"vless-reality": {"dest", "sni", "flow", "fingerprint"}, "vmess": {"cipher"},
	}
}

func TestMetadataExhaustiveAndDefaultsValid(t *testing.T) {
	want := auditedMetadata()
	for _, p := range auditedProtocols(t) {
		metadata, ok := servers.Metadata(p.Name())
		if !ok {
			t.Errorf("missing metadata %s", p.Name())
			continue
		}
		keys := make([]string, 0, len(metadata.Parameters))
		seenKeys := map[string]bool{}
		defaults := map[string]string{}
		if metadata.PublicHost.Name != "public_host" || metadata.PublicHost.Label == "" || metadata.PublicHost.Description == "" {
			t.Errorf("%s incomplete PublicHost metadata", p.Name())
		}
		for _, parameter := range metadata.Parameters {
			keys = append(keys, parameter.Name)
			if seenKeys[parameter.Name] {
				t.Errorf("%s has duplicate parameter key %q", p.Name(), parameter.Name)
			}
			seenKeys[parameter.Name] = true
			if parameter.Name == "" || parameter.Label == "" || parameter.Description == "" {
				t.Errorf("incomplete %s metadata: %#v", p.Name(), parameter)
			}
			if parameter.Name == "public_host" {
				t.Errorf("%s exposes reserved public_host as protocol parameter", p.Name())
			}
			if parameter.Default != "" {
				defaults[parameter.Name] = parameter.Default
			}
			switch parameter.Kind {
			case "text", "number", "choice", "bool", "secret":
			default:
				t.Errorf("%s/%s has unsupported kind %q", p.Name(), parameter.Name, parameter.Kind)
			}
		}
		wantKeys := append([]string(nil), want[p.Name()]...)
		sort.Strings(keys)
		sort.Strings(wantKeys)
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Errorf("%s keys = %v, want %v", p.Name(), keys, wantKeys)
		}
		if err := p.Validate(defaults); err != nil {
			t.Errorf("%s declared defaults rejected: %v", p.Name(), err)
		}
		for _, parameter := range metadata.Parameters {
			if len(parameter.Choices) == 0 {
				continue
			}
			for _, choice := range parameter.Choices {
				candidate := make(map[string]string, len(defaults)+2)
				for key, value := range defaults {
					candidate[key] = value
				}
				candidate[parameter.Name] = choice
				if p.Name() == "snell" && parameter.Name == "obfs" && (choice == "http" || choice == "tls") {
					candidate["obfs_host"] = "obfs.example"
				}
				if err := p.Validate(candidate); err != nil {
					t.Errorf("%s/%s choice %q rejected: %v", p.Name(), parameter.Name, choice, err)
				}
			}
		}
	}
}

func TestMetadataConfigLayerValidation(t *testing.T) {
	for _, p := range auditedProtocols(t) {
		metadata, _ := servers.Metadata(p.Name())
		defaults := map[string]string{}
		for _, parameter := range metadata.Parameters {
			if parameter.Default != "" {
				defaults[parameter.Name] = parameter.Default
			}
		}
		base := config.Default()
		base.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
		base.Servers = []config.Server{{Name: "schema", Protocol: p.Name(), Port: 9000, Accounts: "alice", PublicHost: "node.example", Params: cloneParams(defaults)}}
		if err := base.Validate(); err != nil {
			t.Errorf("%s declared defaults rejected by config validation: %v", p.Name(), err)
		}
		for _, parameter := range metadata.Parameters {
			candidateParams := cloneParams(defaults)
			candidateParams[parameter.Name] = safeParameterValue(p.Name(), parameter.Name, parameter)
			if parameter.Default != "" {
				candidateParams[parameter.Name] = parameter.Default
			}
			if parameter.Name == "cert_file" || parameter.Name == "key_file" {
				candidateParams["cert_file"], candidateParams["key_file"] = "cert.pem", "key.pem"
			}
			if p.Name() == "anytls" && parameter.Name == "padding_scheme" {
				candidateParams[parameter.Name] = "stop=0"
			}
			if p.Name() == "snell" && parameter.Name == "obfs_host" {
				candidateParams["obfs"] = "http"
			}
			if p.Name() == "snell" && (candidateParams["obfs"] == "http" || candidateParams["obfs"] == "tls") {
				candidateParams["obfs_host"] = "obfs.example"
			}
			candidate := config.Default()
			candidate.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
			candidate.Servers = []config.Server{{Name: "schema", Protocol: p.Name(), Port: 9000, Accounts: "alice", PublicHost: "node.example", Params: candidateParams}}
			if err := candidate.Validate(); err != nil {
				t.Errorf("%s/%s rejected by config validation: %v", p.Name(), parameter.Name, err)
			}
		}
		reserved := config.Default()
		reserved.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
		reserved.Servers = []config.Server{{Name: "schema", Protocol: p.Name(), Port: 9000, Accounts: "alice", Params: map[string]string{"public_host": "node.example"}}}
		if err := reserved.Validate(); err == nil {
			t.Errorf("%s config accepted reserved public_host parameter", p.Name())
		}
	}
}

func cloneParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params)+1)
	for key, value := range params {
		out[key] = value
	}
	return out
}

func safeParameterValue(protocol, name string, p servers.Parameter) string {
	if len(p.Choices) != 0 {
		return p.Choices[0]
	}
	if p.Kind == "number" {
		return "1"
	}
	switch name {
	case "sni":
		return "node.example"
	case "cert_file":
		return "cert.pem"
	case "key_file":
		return "key.pem"
	case "alpn":
		if protocol == "tuic" {
			return "h3"
		}
		if protocol == "trusttunnel" {
			return "h2"
		}
		return "h3"
	case "quic_versions":
		return "v1"
	case "padding_scheme":
		return "stop=0\n0=30-30\n1=100-400,c"
	case "obfs_host":
		return "obfs.example"
	case "dest":
		return "www.microsoft.com:443"
	}
	return "value"
}

func TestMetadataReturnsDeepCopy(t *testing.T) {
	first, ok := servers.Metadata("mieru")
	if !ok || len(first.Parameters) == 0 || len(first.Parameters[0].Choices) == 0 {
		t.Fatal("mieru metadata missing")
	}
	first.Parameters[0].Name = "corrupted"
	first.Parameters[0].Choices[0] = "corrupted"
	first.PublicHost.Choices = append(first.PublicHost.Choices, "corrupted")
	second, _ := servers.Metadata("mieru")
	if second.Parameters[0].Name == "corrupted" || second.Parameters[0].Choices[0] == "corrupted" || reflect.DeepEqual(first.PublicHost.Choices, second.PublicHost.Choices) {
		t.Fatal("metadata mutation escaped defensive copy")
	}
}
