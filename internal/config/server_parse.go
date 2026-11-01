package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ktav-lang/golang/pure"
)

// ktavLoadsIntoConfig preserves unknown-at-struct-level protocol keys by
// decoding the top-level document to generic maps before binding known fields.
func ktavLoadsIntoConfig(src string) (*Config, error) {
	cfg := Default()
	if err := ktavLoadsInto(src, cfg); err != nil {
		return nil, configFromBindingError(err)
	}
	// LoadsInto decodes generic numeric map values through encoding/json as float64.
	// Loads preserves integer scalars as int64 so server ports are not lossy.
	rawValue, err := pure.Loads(src)
	if err != nil {
		return nil, configFromBindingError(err)
	}
	raw, ok := rawValue.(map[string]any)
	if !ok {
		return nil, invalid("config must be a table")
	}
	items, hasServers := raw["servers"]
	rawServers, isArray := items.([]any)
	if hasServers && !isArray {
		return nil, invalid("servers must be an array")
	}
	if isArray && len(rawServers) == 0 {
		cfg.Servers = []Server{}
	}
	if len(rawServers) > 0 {
		cfg.Servers = nil
		for i, item := range rawServers {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, invalid("servers[%d] must be a table", i)
			}
			s := Server{Params: map[string]string{}}
			for k, v := range m {
				switch k {
				case "name", "protocol", "bind", "accounts", "public_host":
					text, ok := v.(string)
					if !ok {
						return nil, invalid("servers[%d].%s must be a string", i, k)
					}
					switch k {
					case "name":
						s.Name = text
					case "protocol":
						s.Protocol = text
					case "bind":
						s.Bind = text
					case "accounts":
						s.Accounts = text
					case "public_host":
						s.PublicHost = text
					}
				case "port":
					var err error
					s.Port, err = strconvValue(v)
					if err != nil {
						return nil, invalid("servers[%d].port must be an integer", i)
					}
				default:
					value, ok := protocolParamString(v)
					if !ok {
						return nil, invalid("servers[%d].%s must be a scalar string, boolean, or integer", i, k)
					}
					s.Params[k] = value
				}
			}
			if s.Bind == "" {
				s.Bind = "0.0.0.0"
			}
			cfg.Servers = append(cfg.Servers, s)
		}
	}
	return cfg, nil
}

func protocolParamString(v any) (string, bool) {
	switch value := v.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case int:
		return strconv.Itoa(value), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case uint64:
		return strconv.FormatUint(value, 10), true
	case int32:
		return strconv.FormatInt(int64(value), 10), true
	case uint32:
		return strconv.FormatUint(uint64(value), 10), true
	case int16:
		return strconv.FormatInt(int64(value), 10), true
	case uint16:
		return strconv.FormatUint(uint64(value), 10), true
	case int8:
		return strconv.FormatInt(int64(value), 10), true
	case uint8:
		return strconv.FormatUint(uint64(value), 10), true
	default:
		return "", false
	}
}

func strconvValue(v any) (int, error) {
	switch value := v.(type) {
	case int:
		return value, nil
	case int64:
		if value < 1 || value > 65535 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case uint64:
		if value < 1 || value > 65535 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case int32:
		if value < 1 || value > 65535 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case uint32:
		if value < 1 || value > 65535 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case int16:
		if value < 1 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case uint16:
		if value < 1 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case int8:
		if value < 1 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	case uint8:
		if value < 1 {
			return 0, fmt.Errorf("invalid number")
		}
		return int(value), nil
	default:
		return 0, fmt.Errorf("invalid number")
	}
}

// parseProtocolErrorKey exposes only a parameter key that exactly matches a
// key already present in the supplied parameter map.
func parseProtocolErrorKey(err error, params map[string]string) string {
	msg := strings.ToLower(err.Error())
	const marker = "unknown parameter"
	if i := strings.Index(msg, marker); i >= 0 {
		rest := strings.Trim(strings.TrimSpace(msg[i+len(marker):]), `"' `)
		for key := range params {
			if rest == strings.ToLower(key) {
				return key
			}
		}
		return ""
	}
	for key := range params {
		if strings.Contains(msg, strings.ToLower(key)) {
			return key
		}
	}
	return ""
}
