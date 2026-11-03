package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"golang.org/x/term"
)

type interactiveScreen struct{ title string }

func (s *interactiveScreen) Title() string                        { return s.title }
func (s *interactiveScreen) Init() tea.Cmd                        { return nil }
func (s *interactiveScreen) Update(tea.Msg) (tui.Screen, tea.Cmd) { return s, nil }
func (s *interactiveScreen) View(_, _ int) string                 { return s.title + " not available yet\n" }
func (s *interactiveScreen) KeyHints() string                     { return "q quit" }
func (s *interactiveScreen) CapturingInput() bool                 { return false }

type tuiHeader struct {
	home string
	now  func() time.Time
}

func (h tuiHeader) Header() (tui.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
	defer cancel()
	_, st, err := angelProbeLive(ctx, h.home)
	if errors.Is(err, angel.ErrNotRunning) {
		return tui.Header{Running: false, Version: version}, nil
	}
	if err != nil {
		return tui.Header{}, err
	}
	v := st.Version
	if v == "" {
		v = version
	}
	startedAt, err := time.Parse(time.RFC3339, st.StartedAt)
	if err != nil {
		return tui.Header{Running: true, Listen: st.Listen, Version: v}, nil
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	uptime := now().Sub(startedAt)
	if uptime < 0 {
		uptime = 0
	}
	uptime = uptime / time.Second * time.Second
	return tui.Header{Running: true, Listen: st.Listen, Uptime: uptime, Version: v}, nil
}

var interactiveInputTerminal = func(r io.Reader) bool { f, ok := r.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
var interactiveOutputTerminal = func(w io.Writer) bool { f, ok := w.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
var interactiveRunner = func(w io.Writer, home string, start int) error { return tui.Start(interactiveOptions(w, home, start)) }

func interactiveOptions(w io.Writer, home string, start int) tui.Options {
	tabs := make([]tui.Tab, 0, 3)
	for _, name := range []string{"Providers", "Accounts", "Servers"} {
		n := name
		if n == "Servers" {
			tabs = append(tabs, tui.Tab{Title: n, New: func(env tui.Env) tui.Screen { return newServersScreen(home, env) }})
			continue
		}
		tabs = append(tabs, tui.Tab{Title: n, New: func(tui.Env) tui.Screen { return &interactiveScreen{title: n} }})
	}
	return tui.Options{Tabs: tabs, Start: start, Home: home, Input: os.Stdin, Output: w, Header: tuiHeader{home: home, now: time.Now}, Now: time.Now}
}

func scanInteractiveArgs(args []string) ([]string, bool, error) {
	out := make([]string, 0, len(args))
	enabled := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "-i" || a == "--interactive" {
			enabled = true
			continue
		}
		if a == "-i=true" || a == "--interactive=true" {
			enabled = true
			continue
		}
		if a == "-i=false" || a == "--interactive=false" {
			enabled = false
			continue
		}
		if strings.HasPrefix(a, "-i=") || strings.HasPrefix(a, "--interactive=") {
			name, value, _ := strings.Cut(a, "=")
			v, err := strconv.ParseBool(value)
			if err != nil {
				return nil, false, cli.Usage("invalid value %q for flag %s", value, name)
			}
			enabled = v
			continue
		}
		out = append(out, a)
	}
	return out, enabled, nil
}

const interactiveEntryHint = "use one of: netivri -i; netivri sub -i; netivri account -i; netivri server -i"

func interactiveUsageHint(err error) error {
	var ce *cli.Error
	if errors.As(err, &ce) && ce.Code == "USAGE" {
		copy := *ce
		copy.Hint = interactiveEntryHint
		return &copy
	}
	return err
}
func runInteractive(args []string, stdout io.Writer) error {
	if wantsJSON(args, os.Getenv) {
		return interactiveUsageHint(cli.Usage("--interactive cannot be combined with JSON output"))
	}
	command, commandArgs, err := interactiveCommand(args)
	if err != nil {
		return interactiveUsageHint(err)
	}
	home, rest, err := parseHomeArgs("interactive", commandArgs)
	if err != nil {
		return interactiveUsageHint(err)
	}
	if len(rest) > 0 {
		return interactiveUsageHint(cli.Usage("interactive: unexpected argument %q", rest[0]))
	}
	start := 1
	switch command {
	case "":
	case "sub":
		start = 0
	case "account":
		start = 1
	case "server":
		start = 2
	default:
		return interactiveUsageHint(cli.Usage("interactive mode is not available for %q", command))
	}
	if !interactiveInputTerminal(os.Stdin) || !interactiveOutputTerminal(stdout) {
		return &cli.Error{Code: "NOT_A_TERMINAL", Message: "interactive mode requires a terminal on stdin and stdout", Hint: interactiveEntryHint + "; use plain commands or --json", Exit: cli.ExitUsage}
	}
	return interactiveRunner(stdout, home, start)
}

func interactiveCommand(args []string) (string, []string, error) {
	command := ""
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", nil, cli.Usage("interactive mode does not accept arguments after --")
		}
		if a == "--home" || a == "-home" {
			if i+1 >= len(args) || args[i+1] == "--" {
				return "", nil, cli.Usage("interactive: flag needs an argument: --home")
			}
			out = append(out, a, args[i+1])
			i++
			continue
		}
		if a == "--json" || a == "-json" || a == "--interactive" || a == "-i" || strings.HasPrefix(a, "--interactive=") || strings.HasPrefix(a, "-i=") {
			return "", nil, cli.Usage("interactive: unexpected mode flag %q", a)
		}
		if strings.HasPrefix(a, "--home=") || strings.HasPrefix(a, "-home=") || a == "--local" || a == "-local" || strings.HasPrefix(a, "--local=") || strings.HasPrefix(a, "-local=") {
			out = append(out, a)
			continue
		}
		if strings.HasPrefix(a, "-") {
			return "", nil, cli.Usage("interactive: unknown flag %q", a)
		}
		if command == "" {
			command = a
		} else {
			out = append(out, a)
		}
	}
	return command, out, nil
}
