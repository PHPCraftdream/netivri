package servers_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestMetadataExhaustiveAndDefaultsValid(t *testing.T) {
	want := map[string][]string{
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
	if len(servers.Registered()) != len(want) {
		t.Fatalf("registered count = %d, audited protocols = %d", len(servers.Registered()), len(want))
	}
	for _, p := range servers.Registered() {
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
	for _, protocol := range servers.Registered() {
		metadata, _ := servers.Metadata(protocol.Name())
		defaults := map[string]string{}
		for _, p := range metadata.Parameters {
			if p.Default != "" {
				defaults[p.Name] = p.Default
			}
		}
		base := config.Default()
		base.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
		base.Servers = []config.Server{{Name: "schema", Protocol: protocol.Name(), Port: 9000, Accounts: "alice", PublicHost: "node.example", Params: cloneParams(defaults)}}
		if err := base.Validate(); err != nil {
			t.Errorf("%s declared defaults rejected by config validation: %v", protocol.Name(), err)
		}
		for _, p := range metadata.Parameters {
			candidateParams := cloneParams(defaults)
			candidateParams[p.Name] = safeParameterValue(protocol.Name(), p.Name, p)
			if p.Default != "" {
				candidateParams[p.Name] = p.Default
			}
			if p.Name == "cert_file" || p.Name == "key_file" {
				candidateParams["cert_file"], candidateParams["key_file"] = "cert.pem", "key.pem"
			}
			if protocol.Name() == "anytls" && p.Name == "padding_scheme" {
				candidateParams[p.Name] = "stop=0"
			}
			if protocol.Name() == "snell" && p.Name == "obfs_host" {
				candidateParams["obfs"] = "http"
			}
			if protocol.Name() == "snell" && (candidateParams["obfs"] == "http" || candidateParams["obfs"] == "tls") {
				candidateParams["obfs_host"] = "obfs.example"
			}
			candidate := config.Default()
			candidate.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
			candidate.Servers = []config.Server{{Name: "schema", Protocol: protocol.Name(), Port: 9000, Accounts: "alice", PublicHost: "node.example", Params: candidateParams}}
			if err := candidate.Validate(); err != nil {
				t.Errorf("%s/%s rejected by config validation: %v", protocol.Name(), p.Name, err)
			}
		}
		reserved := config.Default()
		reserved.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
		reserved.Servers = []config.Server{{Name: "schema", Protocol: protocol.Name(), Port: 9000, Accounts: "alice", Params: map[string]string{"public_host": "node.example"}}}
		if err := reserved.Validate(); err == nil {
			t.Errorf("%s config accepted reserved public_host parameter", protocol.Name())
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
