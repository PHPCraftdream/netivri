package auth

import "net"

// RemoteVerifier is implemented by an Authenticator that checks the client
// address before the password is verified (netivri's per-account
// allowed_clients rule). When it is implemented, VerifyWithRemote prefers it.
type RemoteVerifier interface {
	VerifyFrom(user, pass string, remote net.Addr) bool
}

// VerifyWithRemote verifies credentials from remote: VerifyFrom when the
// authenticator implements RemoteVerifier, Verify otherwise.
func VerifyWithRemote(a Authenticator, user, pass string, remote net.Addr) bool {
	if rv, ok := a.(RemoteVerifier); ok {
		return rv.VerifyFrom(user, pass, remote)
	}
	return a.Verify(user, pass)
}
