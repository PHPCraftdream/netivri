package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mihomolog "github.com/metacubex/mihomo/log"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const clashYAML = `proxies:
  - {name: "🇩🇪 Frankfurt 1", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass, udp: true}
  - {name: "Tokyo 01", type: vmess, server: example.invalid, port: 443, uuid: "00000000-0000-0000-0000-000000000000", alterId: 0, cipher: auto, udp: true}
  - {name: "Troj [NL]", type: trojan, server: example.invalid, port: 443, password: pass, udp: true}
  - {name: "剩余流量：88 GB", type: ss, server: 127.0.0.1, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "Expire: 2026-01-01", type: ss, server: example.invalid, port: 0, cipher: aes-128-gcm, password: pass}
  - {name: "Broken", type: nope, server: example.invalid, port: 1}
  - {name: "🇩🇪 Frankfurt 1", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass, udp: true}
`

func TestParseClashYAML(t *testing.T) {
	res, err := ParseDetailed([]byte(clashYAML))
	if err != nil {
		t.Fatalf("ParseDetailed: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(res.Nodes))
	}
	if res.Skipped.Banner != 2 || res.Skipped.Invalid != 1 {
		t.Fatalf("skips = %+v, want banner 2 invalid 1", res.Skipped)
	}
	want := []struct {
		typ, country string
		port         int
	}{
		{"ss", "DE", 8388},
		{"vmess", "JP", 443},
		{"trojan", "NL", 443},
	}
	for i, w := range want {
		n := res.Nodes[i]
		if n.Type != w.typ || n.Country != w.country || n.Port != w.port {
			t.Errorf("node %d = %+v, want type %s country %s port %d", i, n, w.typ, w.country, w.port)
		}
		if n.Server != "example.invalid" || n.Name == "" {
			t.Errorf("node %d has unexpected server/name: %+v", i, n)
		}
		if n.ID == "" || len(n.ID) != 12 {
			t.Errorf("node %d bad id %q", i, n.ID)
		}
		if n.ID != NodeID(n) {
			t.Errorf("node %d id not derived from fields", i)
		}
		if n.Raw == nil || n.Raw["cipher"] != "aes-128-gcm" && n.Type == "ss" {
			t.Errorf("node %d raw mapping lost", i)
		}
	}
	// Order follows the provider order.
	if res.Nodes[0].Type != "ss" || res.Nodes[1].Type != "vmess" || res.Nodes[2].Type != "trojan" {
		t.Fatalf("nodes out of order: %+v", res.Nodes)
	}
	// Raw must never leak through the node JSON.
	data, _ := json.Marshal(res.Nodes[0])
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "cipher") {
		t.Fatalf("node JSON leaks raw mapping: %s", data)
	}
}

func TestParseBase64URIList(t *testing.T) {
	list := "not-a-uri\n" +
		"ss://YWVzLTEyOC1nY206cGFzcw==@example.invalid:8388#DE%20Node\n" +
		"trojan://pass@example.invalid:443#Troj%20NL\n"
	body := base64.StdEncoding.EncodeToString([]byte(list))
	res, err := ParseDetailed([]byte(body))
	if err != nil {
		t.Fatalf("ParseDetailed: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(res.Nodes))
	}
	if res.Nodes[0].Name != "DE Node" || res.Nodes[0].Country != "DE" || res.Nodes[0].Port != 8388 {
		t.Errorf("node 0 = %+v", res.Nodes[0])
	}
	if res.Nodes[1].Name != "Troj NL" || res.Nodes[1].Country != "NL" || res.Nodes[1].Type != "trojan" {
		t.Errorf("node 1 = %+v", res.Nodes[1])
	}
	if res.Skipped.Invalid != 0 {
		t.Errorf("garbage line counted as invalid: %+v", res.Skipped)
	}
}

func TestParsePlainURIList(t *testing.T) {
	vmJSON := `{"ps":"VM 1","add":"example.invalid","port":"443","id":"00000000-0000-0000-0000-000000000000","aid":"0","net":"tcp"}`
	list := "vmess://" + base64.StdEncoding.EncodeToString([]byte(vmJSON)) + "\n" +
		"ss://aes-128-gcm:pass@example.invalid:8388#DE%201\n"
	res, err := ParseDetailed([]byte(list))
	if err != nil {
		t.Fatalf("ParseDetailed: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(res.Nodes))
	}
	if res.Nodes[0].Name != "VM 1" || res.Nodes[0].Type != "vmess" || res.Nodes[0].Port != 443 {
		t.Errorf("node 0 = %+v", res.Nodes[0])
	}
	if res.Nodes[1].Name != "DE 1" || res.Nodes[1].Type != "ss" {
		t.Errorf("node 1 = %+v", res.Nodes[1])
	}
}

func subCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var e *cli.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not *cli.Error: %v", err)
	}
	return e.Code
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "   \n  ", "hello world", "port: 7890\n"} {
		if _, err := Parse([]byte(in)); subCode(t, err) != "SUBSCRIPTION_EMPTY" {
			t.Errorf("input %q: code = %s, want SUBSCRIPTION_EMPTY", in, subCode(t, err))
		}
	}
	_, err := Parse([]byte("vmess://!!!!\n"))
	if subCode(t, err) != "SUBSCRIPTION_EMPTY" {
		t.Errorf("garbage URI: code = %s, want SUBSCRIPTION_EMPTY", subCode(t, err))
	}
	_, err = Parse(make([]byte, maxInput+1))
	if subCode(t, err) != "SUBSCRIPTION_TOO_LARGE" {
		t.Errorf("oversize: code = %s, want SUBSCRIPTION_TOO_LARGE", subCode(t, err))
	}
	var b strings.Builder
	b.WriteString("proxies:\n")
	for i := 0; i < maxNodes+1; i++ {
		b.WriteString("- {name: n, type: ss, server: e.invalid, port: 1, cipher: aes-128-gcm, password: p}\n")
	}
	_, err = Parse([]byte(b.String()))
	if subCode(t, err) != "SUBSCRIPTION_TOO_MANY" {
		t.Errorf("too many nodes: code = %s, want SUBSCRIPTION_TOO_MANY", subCode(t, err))
	}
	if cli.ExitNetwork != 3 {
		t.Fatal("exit code contract changed")
	}
}

func TestNodeIDStability(t *testing.T) {
	base := Node{Name: "A", Type: "ss", Server: "h", Port: 1}
	other := []Node{
		{Name: "A", Type: "ss", Server: "h", Port: 1},
		{Name: "B", Type: "ss", Server: "h", Port: 1},
		{Name: "A", Type: "vmess", Server: "h", Port: 1},
		{Name: "A", Type: "ss", Server: "i", Port: 1},
		{Name: "A", Type: "ss", Server: "h", Port: 2},
	}
	id := NodeID(base)
	if id != NodeID(other[0]) {
		t.Error("identical nodes must have identical ids")
	}
	if len(id) != 12 {
		t.Errorf("id length = %d, want 12", len(id))
	}
	seen := map[string]bool{id: true}
	for _, o := range other[1:] {
		nid := NodeID(o)
		if seen[nid] {
			t.Errorf("id collision for %+v", o)
		}
		seen[nid] = true
	}
}

// When every entry is rejected the per-reason counts still reach the caller.
func TestParseDetailedKeepsSkipCountsOnError(t *testing.T) {
	body := "proxies:\n" +
		"  - {name: \"Expire: 2026-01-01\", type: ss, server: example.invalid, port: 0, cipher: aes-128-gcm, password: pass}\n" +
		"  - {name: \"Broken\", type: nope, server: example.invalid, port: 1}\n"
	res, err := ParseDetailed([]byte(body))
	if subCode(t, err) != "SUBSCRIPTION_EMPTY" {
		t.Fatalf("code = %s, want SUBSCRIPTION_EMPTY", subCode(t, err))
	}
	if res.Skipped.Banner != 1 || res.Skipped.Invalid != 1 {
		t.Fatalf("skips = %+v, want banner 1 invalid 1", res.Skipped)
	}
}

// The converter prints raw share links (credentials) through Mihomo's global
// logger on parse failures: parsing must silence it.
func TestParseSilencesConverterLog(t *testing.T) {
	_, _ = Parse([]byte("vless://user@example.invalid\n"))
	if mihomolog.Level() != mihomolog.SILENT {
		t.Fatalf("Mihomo log level = %v after Parse, want SILENT", mihomolog.Level())
	}
}
