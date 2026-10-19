// Package subscription parses, identifies and stores provider
// subscriptions: raw bodies from a subscription URL become a validated
// list of nodes with stable ids and best-effort exit countries.
// Fetching itself lives in internal/fetch; this package only sees bytes.
package subscription

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	mihomolog "github.com/metacubex/mihomo/log"
	logrus "github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v3"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const (
	// maxInput caps the raw subscription body size.
	maxInput = 16 << 20
	// maxNodes caps how many proxy entries are processed.
	maxNodes = 5000
)

// Node is one proxy from a subscription.
type Node struct {
	ID      string `json:"id"`   // first 12 hex chars of sha256(type|server|port|name)
	Name    string `json:"name"` // as given by the provider
	Type    string `json:"type"` // ss, vmess, vless, trojan, hysteria2, tuic, ...
	Server  string `json:"server"`
	Port    int    `json:"port"`
	Country string `json:"country,omitempty"` // ISO 3166-1 alpha-2, "" when unknown
	// Raw is the full proxy mapping; it carries credentials and is never
	// serialised with the node itself.
	Raw map[string]any `json:"-"`
}

// Skips counts entries dropped by ParseDetailed, by reason.
type Skips struct {
	Invalid int `json:"invalid"` // failed Mihomo validation
	Banner  int `json:"banner"`  // traffic/expiry pseudo-nodes
}

// ParseResult is the outcome of a parse.
type ParseResult struct {
	Nodes   []Node `json:"nodes"`
	Skipped Skips  `json:"skipped"`
}

var logOnce sync.Once

// silenceConverterLog stops Mihomo's share-link converter from printing raw
// subscription lines (they carry credentials) through its global logger,
// which writes to stdout by default.
func silenceConverterLog() {
	logOnce.Do(func() {
		logrus.SetOutput(io.Discard)
		logrus.SetLevel(logrus.PanicLevel)
		mihomolog.SetLevel(mihomolog.SILENT)
	})
}

// Parse turns a subscription body into valid nodes. It returns a
// SUBSCRIPTION_EMPTY error when no valid node remains.
func Parse(content []byte) ([]Node, error) {
	res, err := ParseDetailed(content)
	if err != nil {
		return nil, err
	}
	return res.Nodes, nil
}

// ParseDetailed is Parse with the per-reason skip counts, which are also
// returned alongside a SUBSCRIPTION_EMPTY error when every entry was
// rejected.
func ParseDetailed(content []byte) (ParseResult, error) {
	silenceConverterLog()
	if len(content) > maxInput {
		return ParseResult{}, subError("SUBSCRIPTION_TOO_LARGE", "subscription body exceeds 16 MiB")
	}

	mappings, ok := parseClashYAML(content)
	if !ok {
		var err error
		mappings, err = convert.ConvertsV2Ray(content)
		if err != nil {
			return ParseResult{}, subError("SUBSCRIPTION_EMPTY", "no nodes found in subscription body")
		}
	}
	if len(mappings) == 0 {
		return ParseResult{}, subError("SUBSCRIPTION_EMPTY", "no nodes found in subscription body")
	}
	if len(mappings) > maxNodes {
		return ParseResult{}, subError("SUBSCRIPTION_TOO_MANY", "subscription has more than 5000 nodes")
	}

	var res ParseResult
	seen := make(map[string]struct{}, len(mappings))
	for _, m := range mappings {
		name, _ := m["name"].(string)
		typ, _ := m["type"].(string)
		server, _ := m["server"].(string)
		port, portOK := toInt(m["port"])

		if isBanner(name, server, port, portOK) {
			res.Skipped.Banner++
			continue
		}
		if name == "" {
			res.Skipped.Invalid++
			continue
		}
		if _, err := adapter.ParseProxy(m); err != nil {
			res.Skipped.Invalid++
			continue
		}

		node := Node{
			Name:    name,
			Type:    typ,
			Server:  server,
			Port:    port,
			Country: CountryFromName(name),
			Raw:     m,
		}
		node.ID = NodeID(node)
		if _, dup := seen[node.ID]; dup {
			continue
		}
		seen[node.ID] = struct{}{}
		res.Nodes = append(res.Nodes, node)
	}
	if len(res.Nodes) == 0 {
		return res, subError("SUBSCRIPTION_EMPTY", "no valid nodes in subscription body")
	}
	return res, nil
}

// parseClashYAML detects a Clash/Mihomo document with a proxies list.
// It reports ok=false for anything else so the caller can try the URI
// formats.
func parseClashYAML(content []byte) ([]map[string]any, bool) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, false
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(trimmed, &doc); err != nil || len(doc.Proxies) == 0 {
		return nil, false
	}
	return doc.Proxies, true
}

// isBanner reports traffic/expiry pseudo-nodes that providers inject.
func isBanner(name, server string, port int, portOK bool) bool {
	lower := strings.ToLower(name)
	for _, s := range bannerTerms {
		if strings.Contains(lower, s) {
			return true
		}
	}
	switch server {
	case "127.0.0.1", "0.0.0.0", "::1", "localhost":
		return true
	}
	if portOK && port <= 0 {
		return true
	}
	return false
}

var bannerTerms = []string{
	"traffic", "expire", "expiry", "valid until", "remaining", "剩余", "流量", "过期",
	"到期", "官网", "套餐", "电报", "客服", "重置", "telegram", "t.me",
}

// NodeID derives the stable node id from its identifying fields.
func NodeID(n Node) string {
	sum := sha256.Sum256([]byte(n.Type + "|" + n.Server + "|" + strconv.Itoa(n.Port) + "|" + n.Name))
	return hex.EncodeToString(sum[:])[:12]
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	default:
		return 0, false
	}
}

func subError(code, msg string) *cli.Error {
	return &cli.Error{Code: code, Message: msg, Exit: cli.ExitNetwork}
}
