package main

import (
	"encoding/json"
	"strconv"
	"testing"
)

type serverStatusSummary struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Listen         string `json:"listen"`
	Connections    int    `json:"connections"`
	DroppedClients int    `json:"dropped_clients"`
}

type serverStatusEnvelope struct {
	Data struct {
		Servers []serverStatusSummary `json:"servers"`
	} `json:"data"`
}

func serverStatusMatches(raw, name, protocol string, wantPresent bool) (bool, error) {
	var env serverStatusEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return false, err
	}
	present := false
	for _, server := range env.Data.Servers {
		if server.Name != name {
			continue
		}
		present = true
		if wantPresent && (server.Protocol != protocol || server.Listen == "" || server.Connections < 0 || server.DroppedClients < 0) {
			return false, nil
		}
	}
	return present == wantPresent, nil
}

func TestServerStatusMatchesRequestedIdentity(t *testing.T) {
	const status = `{"data":{"servers":[{"name":"exit-direct-edge","protocol":"vmess","listen":"127.0.0.1:1234","connections":0,"dropped_clients":0}]}}`
	const empty = `{"data":{"servers":[]}}`
	for _, tc := range []struct {
		name, protocol string
		status         string
		present        bool
		want           bool
	}{
		{"edge", "shadowsocks", status, true, false},
		{"exit-direct-edge", "shadowsocks", status, true, false},
		{"exit-direct-edge", "vmess", status, true, true},
		{"exit-direct-edge", "vmess", status, false, false},
		{"exit-direct-edge", "vmess", empty, false, true},
		{"exit-direct-edge", "vmess", empty, true, false},
		{"other", "vmess", status, false, true},
		{"exit-direct-edge", "vmess", `{"data":{"servers":[{"name":"exit-direct-edge","protocol":"vmess","listen":"","connections":-1,"dropped_clients":0}]}}`, true, false},
	} {
		t.Run(tc.name+"/present="+strconv.FormatBool(tc.present), func(t *testing.T) {
			got, err := serverStatusMatches(tc.status, tc.name, tc.protocol, tc.present)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("match=%t want %t", got, tc.want)
			}
		})
	}
	if _, err := serverStatusMatches(`{`, "edge", "vmess", true); err == nil {
		t.Fatal("malformed status should fail decoding")
	}
}
