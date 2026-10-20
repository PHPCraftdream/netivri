//go:build bench

package core

import (
	"fmt"
	"strconv"
	"testing"

	auth "github.com/metacubex/mihomo/component/auth"
)

// BenchmarkConnectRoutedAccounts measures a routed connect with 1, 100 and
// 2000 accounts. Every account has its own IN-USER rule (accounts.go), so the
// last login in rule order is the worst case of the rule scan: a per-account
// cost would show as a growing time per connect.
func BenchmarkConnectRoutedAccounts(b *testing.B) {
	for _, accounts := range []int{1, 100, 2000} {
		b.Run(strconv.Itoa(accounts), func(b *testing.B) {
			backend := benchEchoBackend(b)
			upstream := benchFakeUpstream(b)
			users := make([]auth.AuthUser, accounts)
			routes := make(map[string]string, accounts)
			for i := range users {
				login := fmt.Sprintf("acct%05d", i)
				users[i] = auth.AuthUser{User: login, Pass: "bench"}
				routes[login] = "bench"
			}
			// Rules follow the sorted logins: the highest one is matched last.
			last := fmt.Sprintf("acct%05d", accounts-1)
			inst := benchStart(b, Options{
				HomeDir:       b.TempDir(),
				Authenticator: auth.NewAuthenticator(users),
				RouteNodes: map[string][]map[string]any{
					"bench": {benchNodeMap(upstream.Listener)},
				},
				AccountRoute: routes,
			})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := benchConnectOnce(inst.Addr(), backend, last, "bench"); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			benchRouteHits(b, int64(b.N), upstream)
		})
	}
}
