package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultHome returns the netivri state directory: $NETIVRI_HOME if
// set, otherwise <UserConfigDir>/netivri.
func DefaultHome() string {
	if h := os.Getenv("NETIVRI_HOME"); h != "" {
		return h
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return filepath.Join(base, "netivri")
}

// ConfigPath returns the configuration file path inside a home
// directory.
func ConfigPath(home string) string {
	return filepath.Join(home, "config.ktav")
}

// Redact turns a secret-bearing URL into a safe one-line form:
// subscription URLs become scheme://host/**** (path and query dropped)
// and proxy URLs with credentials keep only the username with the
// password replaced. Anything unparsable becomes "****".
func Redact(u string) string {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "****"
	}
	if parsed.User != nil {
		return parsed.Scheme + "://" + parsed.User.Username() + ":****@" + parsed.Host
	}
	return parsed.Scheme + "://" + parsed.Host + "/****"
}
