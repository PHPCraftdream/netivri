package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// localHomeName is the state directory that --local creates in the current
// directory and that a command finds there without any flag.
const localHomeName = ".netivri"

// Where a state directory came from, as `status` reports it.
const (
	homeFromFlag      = "--home"
	homeFromLocalFlag = "--local"
	homeFromLocalDir  = "./" + localHomeName
	homeFromEnv       = "NETIVRI_HOME"
	homeFromDefault   = "user config directory"
)

// getwd is os.Getwd; tests change the directory instead of replacing it.
var getwd = os.Getwd

// resolveHome picks the state directory, the one place that does it for `up`
// and for every other command. In order:
//
//  1. --home {DIR}, as given;
//  2. --local: .netivri in the current directory, created when missing;
//  3. a .netivri directory in the current directory (only there, parents are
//     not searched);
//  4. $NETIVRI_HOME;
//  5. the user config directory plus /netivri.
//
// --home and --local together are a usage error. The second result says which
// of these it was.
func resolveHome(flagHome string, local bool, env func(string) string, userConfigDir func() (string, error)) (string, string, error) {
	if flagHome != "" && local {
		return "", "", cli.Usage("--home and --local cannot be combined: --home names the state directory, --local uses ./%s", localHomeName)
	}
	if flagHome != "" {
		return flagHome, homeFromFlag, nil
	}
	cwd, cwdErr := getwd()
	if local {
		if cwdErr != nil {
			return "", "", &cli.Error{Code: "INTERNAL", Message: "resolve the current directory: " + cwdErr.Error(), Exit: cli.ExitInternal}
		}
		dir := filepath.Join(cwd, localHomeName)
		if err := ensureLocalHome(dir); err != nil {
			return "", "", &cli.Error{Code: "INTERNAL", Message: "prepare the state directory ./" + localHomeName + ": " + err.Error(), Exit: cli.ExitInternal}
		}
		return dir, homeFromLocalFlag, nil
	}
	if cwdErr == nil {
		dir := filepath.Join(cwd, localHomeName)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, homeFromLocalDir, nil
		}
	}
	home, err := defaultHome(env, userConfigDir)
	if err != nil {
		return "", "", &cli.Error{Code: "INTERNAL", Message: "resolve home directory: " + err.Error(), Exit: cli.ExitInternal}
	}
	if env("NETIVRI_HOME") != "" {
		return home, homeFromEnv, nil
	}
	return home, homeFromDefault, nil
}

// ensureLocalHome creates the directory with owner-only access and, in a new
// one, a .gitignore that ignores everything in it: it holds subscription URLs
// and the control token, which must not be committed by accident. An existing
// directory is left as it is.
func ensureLocalHome(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return errors.New("it exists and is not a directory")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600)
}
