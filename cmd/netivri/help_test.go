package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func dispatchOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := dispatch(args, &out)
	return out.String(), err
}

func mustDispatch(t *testing.T, args ...string) string {
	t.Helper()
	out, err := dispatchOut(t, args...)
	if err != nil {
		t.Fatalf("dispatch(%q) = %v, want nil", args, err)
	}
	return out
}

func decodeHelp(t *testing.T, out string) map[string]any {
	t.Helper()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("want exactly one JSON line, got %q", out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	if env["schema_version"] != float64(cli.SchemaVersion) || env["ok"] != true {
		t.Fatalf("bad envelope: %v", env)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object: %v", env)
	}
	help, ok := data["help"].(map[string]any)
	if !ok {
		t.Fatalf("no data.help object: %v", data)
	}
	return help
}

func TestHelpEntryPoints(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}, {"-help"}} {
		out := mustDispatch(t, args...)
		for _, want := range []string{"USAGE", "QUICK START", "COMMANDS", "GLOBAL FLAGS", "EXIT CODES", "netivri help {COMMAND}"} {
			if !strings.Contains(out, want) {
				t.Errorf("dispatch(%q) lacks %q:\n%s", args, want, out)
			}
		}
	}
}

func TestHelpOverviewListsEveryCommandAndExample(t *testing.T) {
	out := mustDispatch(t, "help")
	for _, c := range listedCommands() {
		if !strings.Contains(out, c.Name) {
			t.Errorf("overview does not list %q", c.Name)
		}
	}
	for _, want := range []string{"netivri sub add", "netivri account add", "netivri up", "netivri status", "NOT IMPLEMENTED YET", "node test"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
}

// conceptTopics are the help topics that are not commands: concepts about
// configuring and operating netivri.
var conceptTopics = []string{"config", "routing", "output"}

func TestHelpTopicsAgreeWithTheCommandTable(t *testing.T) {
	seen := map[string]bool{}
	for _, tp := range helpTopics {
		if seen[tp.Name] {
			t.Errorf("duplicate help topic %q", tp.Name)
		}
		seen[tp.Name] = true
		first := strings.Fields(tp.Name)[0]
		if _, ok := commands[first]; !ok && tp.Name != "help" && !contains(conceptTopics, tp.Name) {
			t.Errorf("topic %q names no command", tp.Name)
		}
		if tp.Summary == "" || len(tp.Usage) == 0 {
			t.Errorf("topic %q needs a summary and usage", tp.Name)
		}
		if tp.Group != "" {
			if !contains(helpGroups, tp.Group) {
				t.Errorf("topic %q has unknown group %q", tp.Name, tp.Group)
			}
			if len(tp.Examples) == 0 || tp.Description == "" {
				t.Errorf("listed topic %q needs a description and examples", tp.Name)
			}
		} else if len(subTopics(tp.Name)) == 0 {
			t.Errorf("unlisted topic %q has no subcommands", tp.Name)
		}
	}
	for name := range commands {
		if findHelpTopic(name) == nil {
			t.Errorf("command %q has no help topic", name)
		}
		out := mustDispatch(t, name, "--help")
		if !strings.HasPrefix(out, "netivri "+name+" - ") {
			t.Errorf("%s --help starts with %q", name, firstLine(out))
		}
	}
	if findHelpTopic("help") == nil {
		t.Error("no help topic for help itself")
	}
}

func TestHelpSubcommandForms(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"sub", "add", "--help"}, "netivri sub add - "},
		{[]string{"account", "set-country", "-h"}, "netivri account set-country - "},
		{[]string{"node", "list", "--help"}, "netivri node list - "},
		{[]string{"diag", "dial", "--help"}, "netivri diag dial - "},
		{[]string{"stats", "--help"}, "netivri stats - "},
		{[]string{"up", "--port", "0", "--help"}, "netivri up - "},
		{[]string{"help", "sub", "add"}, "netivri sub add - "},
		{[]string{"help", "help"}, "netivri help - "},
		{[]string{"help", "config"}, "netivri config - "},
		{[]string{"help", "routing"}, "netivri routing - "},
		{[]string{"help", "output"}, "netivri output - "},
		{[]string{"sub", "--help"}, "netivri sub - "},
		{[]string{"account", "--help"}, "netivri account - "},
	}
	for _, c := range cases {
		out := mustDispatch(t, c.args...)
		if !strings.HasPrefix(out, c.want) {
			t.Errorf("dispatch(%q) starts with %q, want %q", c.args, firstLine(out), c.want)
		}
	}
	group := mustDispatch(t, "help", "sub")
	for _, want := range []string{"COMMANDS", "sub add", "sub countries", "netivri help sub {COMMAND}"} {
		if !strings.Contains(group, want) {
			t.Errorf("group help lacks %q:\n%s", want, group)
		}
	}
}

func TestHelpFlagNeverRunsTheCommand(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"up", "--home", home, "--help"},
		{"up", "--foreground", "--home", home, "-h"},
		{"sub", "add", "https://provider.example/sub/x", "--home", home, "--help"},
		{"account", "add", "alice", "--home", home, "--help"},
		{"down", "--home", home, "--help"},
	} {
		mustDispatch(t, args...)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("help created files in the state directory: %v", entries)
	}
}

func TestHelpAfterDoubleDashIsNotHelp(t *testing.T) {
	if _, ok := parseHelpRequest([]string{"sub", "add", "--", "--help"}); ok {
		t.Error("--help after a bare -- must not be a help request")
	}
}

func TestParseHelpRequest(t *testing.T) {
	cases := []struct {
		args  []string
		ok    bool
		topic []string
		json  bool
	}{
		{nil, true, nil, false},
		{[]string{"--json"}, true, nil, true},
		{[]string{"help"}, true, nil, false},
		{[]string{"help", "up", "--json"}, true, []string{"up"}, true},
		{[]string{"--json", "help", "sub", "add"}, true, []string{"sub", "add"}, true},
		{[]string{"sub", "add", "--help", "--json"}, true, []string{"sub", "add"}, true},
		{[]string{"up", "--help"}, true, []string{"up"}, false},
		{[]string{"stats", "--by", "country", "-h"}, true, []string{"stats"}, false},
		{[]string{"up"}, false, nil, false},
		{[]string{"up", "--port", "0"}, false, nil, false},
		{[]string{"frobnicate", "--help"}, false, nil, false},
		{[]string{"version"}, false, nil, false},
	}
	for _, c := range cases {
		req, ok := parseHelpRequest(c.args)
		if ok != c.ok {
			t.Errorf("parseHelpRequest(%q) ok = %v, want %v", c.args, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if !reflect.DeepEqual(req.topic, c.topic) || req.json != c.json {
			t.Errorf("parseHelpRequest(%q) = topic %v json %v, want %v %v", c.args, req.topic, req.json, c.topic, c.json)
		}
	}
}

func TestHelpJSONOverview(t *testing.T) {
	help := decodeHelp(t, mustDispatch(t, "help", "--json"))
	if help["kind"] != "overview" {
		t.Fatalf("kind = %v", help["kind"])
	}
	cmds, _ := help["commands"].([]any)
	names := map[string]bool{}
	for _, c := range cmds {
		m := c.(map[string]any)
		names[m["name"].(string)] = true
		if u, _ := m["usage"].([]any); len(u) == 0 {
			t.Errorf("command %v has no usage", m["name"])
		}
	}
	for _, want := range []string{"up", "down", "status", "sub add", "account add", "stats", "diag dial", "version", "help"} {
		if !names[want] {
			t.Errorf("overview JSON lacks command %q", want)
		}
	}
	if codes, _ := help["exit_codes"].([]any); len(codes) != 8 {
		t.Errorf("exit_codes = %v", help["exit_codes"])
	}
	if planned, _ := help["planned"].([]any); len(planned) == 0 {
		t.Error("no planned commands listed")
	}
	if flags, _ := help["global_flags"].([]any); len(flags) == 0 {
		t.Error("no global flags listed")
	}
	for _, args := range [][]string{{"--json"}, {"--json", "help"}, {"help", "--json"}} {
		if got := decodeHelp(t, mustDispatch(t, args...)); got["kind"] != "overview" {
			t.Errorf("dispatch(%q) kind = %v", args, got["kind"])
		}
	}
}

func TestHelpJSONTopic(t *testing.T) {
	for _, args := range [][]string{
		{"help", "up", "--json"},
		{"up", "--help", "--json"},
		{"--json", "help", "up"},
	} {
		help := decodeHelp(t, mustDispatch(t, args...))
		if help["kind"] != "topic" || help["name"] != "up" {
			t.Errorf("dispatch(%q) = %v", args, help)
		}
		if ex, _ := help["examples"].([]any); len(ex) == 0 {
			t.Errorf("dispatch(%q): no examples", args)
		}
		if er, _ := help["errors"].([]any); len(er) == 0 {
			t.Errorf("dispatch(%q): no errors", args)
		}
	}
	group := decodeHelp(t, mustDispatch(t, "help", "sub", "--json"))
	if subs, _ := group["subcommands"].([]any); len(subs) != 5 {
		t.Errorf("sub subcommands = %v, want 5", group["subcommands"])
	}
}

func TestHelpErrors(t *testing.T) {
	for _, args := range [][]string{
		{"help", "nope"},
		{"help", "sub", "nope"},
		{"help", "sub", "add", "extra"},
		{"help", "--nope"},
	} {
		out, err := dispatchOut(t, args...)
		ce, ok := err.(*cli.Error)
		if !ok {
			t.Errorf("dispatch(%q) = %T (%v), want *cli.Error", args, err, err)
			continue
		}
		if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
			t.Errorf("dispatch(%q) code/exit = %q/%d", args, ce.Code, ce.Exit)
		}
		if !strings.Contains(ce.Hint, "netivri help") {
			t.Errorf("dispatch(%q) hint = %q", args, ce.Hint)
		}
		if out != "" {
			t.Errorf("dispatch(%q) wrote to stdout on failure: %q", args, out)
		}
	}
}

func TestHelpTextShape(t *testing.T) {
	texts := map[string]string{"overview": renderOverview()}
	for i := range helpTopics {
		texts[helpTopics[i].Name] = renderTopic(&helpTopics[i])
	}
	for name, text := range texts {
		if !strings.HasSuffix(text, "\n") {
			t.Errorf("%s: no trailing newline", name)
		}
		if strings.Contains(text, "%!") {
			t.Errorf("%s: formatting error in the text", name)
		}
		for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			where := name + ":" + strconv.Itoa(i+1)
			for _, r := range line {
				if r >= 0x80 || r == '\t' {
					t.Errorf("%s: non-ASCII or tab in %q", where, line)
					break
				}
			}
			if strings.HasSuffix(line, " ") {
				t.Errorf("%s: trailing space in %q", where, line)
			}
			trim := strings.TrimSpace(line)
			literal := strings.HasPrefix(trim, "netivri ") || strings.HasPrefix(trim, "curl ") || strings.HasPrefix(trim, "# ")
			switch {
			case len(line) > 100:
				t.Errorf("%s: line of %d columns: %q", where, len(line), line)
			case len(line) > helpWidth && !literal:
				t.Errorf("%s: wrapped text of %d columns: %q", where, len(line), line)
			}
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap("aaa bbb ccc", 2, 8)
	if want := []string{"  aaa", "  bbb", "  ccc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	got = wrap("one two\n\nthree", 0, 40)
	if want := []string{"one two", "", "three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paragraphs = %q, want %q", got, want)
	}
	got = wrap("short supercalifragilistic", 0, 10)
	if want := []string{"short", "supercalifragilistic"}; !reflect.DeepEqual(got, want) {
		t.Errorf("long word = %q, want %q", got, want)
	}
	if got := wrap("   ", 2, 10); len(got) != 0 {
		t.Errorf("blank text wrapped to %q", got)
	}
}

func TestWriteRowsHangingIndent(t *testing.T) {
	var b strings.Builder
	writeRows(&b, [][2]string{
		{"--a", "first meaning that is long enough to wrap onto a second line of output"},
		{"--long-label-that-is-far-too-wide-for-the-column", "goes below"},
	}, 2, 40)
	want := "  --a  first meaning that is long enough\n" +
		"       to wrap onto a second line of\n" +
		"       output\n" +
		"  --long-label-that-is-far-too-wide-for-the-column\n" +
		"       goes below\n"
	if got := b.String(); got != want {
		t.Errorf("writeRows =\n%s\nwant\n%s", got, want)
	}
}

// flagTokens returns the flags a usage or example line mentions.
func flagTokens(line string) []string {
	var out []string
	for _, tok := range strings.Fields(line) {
		tok = strings.Trim(tok, "[]|,")
		if i := strings.Index(tok, "="); i >= 0 {
			tok = tok[:i]
		}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			out = append(out, tok)
		}
	}
	return out
}

// topicFor resolves "netivri <words...>" to the longest matching topic.
func topicFor(line string) *helpTopic {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "netivri" {
		return nil
	}
	if len(f) >= 3 {
		if t := findHelpTopic(f[1] + " " + f[2]); t != nil {
			return t
		}
	}
	return findHelpTopic(f[1])
}

func documentedFlags(t *helpTopic) map[string]bool {
	ok := map[string]bool{"--home": true, "--json": true, "--quiet": true, "--no-color": true, "--help": true, "-h": true}
	for _, f := range t.Flags {
		for _, part := range strings.Split(f.Name, ",") {
			if fields := strings.Fields(part); len(fields) > 0 {
				ok[fields[0]] = true
			}
		}
	}
	return ok
}

// A usage line or an example that names an undocumented flag or an unknown
// command is documentation drift; this is the cheap guard against it.
func TestHelpUsageAndExamplesUseDocumentedFlags(t *testing.T) {
	check := func(where, line string) {
		// {COMMAND} is a placeholder in the help topic's own second usage line.
		if !strings.HasPrefix(line, "netivri ") || strings.HasPrefix(line, "netivri {COMMAND}") {
			return
		}
		tp := topicFor(line)
		if tp == nil {
			t.Errorf("%s: %q names no help topic", where, line)
			return
		}
		allowed := documentedFlags(tp)
		for _, f := range flagTokens(line) {
			if !allowed[f] {
				t.Errorf("%s: %q uses %s, which topic %q does not document", where, line, f, tp.Name)
			}
		}
	}
	for i := range helpTopics {
		tp := &helpTopics[i]
		for _, u := range tp.Usage {
			check(tp.Name+" usage", u)
		}
		for _, e := range tp.Examples {
			if e.Comment == "" || e.Command == "" {
				t.Errorf("%s: example needs a comment and a command", tp.Name)
			}
			for _, line := range strings.Split(e.Command, "\n") {
				check(tp.Name+" example", line)
			}
		}
	}
	for _, q := range helpQuickStart {
		check("quick start", q)
	}
}

var cliDocErrorRow = regexp.MustCompile("^\\| `([A-Z_]+)` \\| ([0-9]) \\|")

// Every error the help lists must exist in docs/cli.md with the same exit
// code, and the exit codes listed must be the constants of the cli package.
func TestHelpErrorsMatchTheDocumentedContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatalf("read docs/cli.md: %v", err)
	}
	documented := map[string]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := cliDocErrorRow.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			documented[m[1]] = n
		}
	}
	if len(documented) < 20 {
		t.Fatalf("parsed only %d error codes from docs/cli.md", len(documented))
	}
	for _, tp := range helpTopics {
		for _, e := range tp.Errors {
			exit, ok := documented[e.Code]
			if !ok {
				t.Errorf("%s: error %s is not in docs/cli.md", tp.Name, e.Code)
			} else if exit != e.Exit {
				t.Errorf("%s: error %s has exit %d, docs/cli.md says %d", tp.Name, e.Code, e.Exit, exit)
			}
		}
	}
	want := []int{cli.ExitOK, cli.ExitInternal, cli.ExitUsage, cli.ExitNetwork, cli.ExitNoAngel, cli.ExitNotFound, cli.ExitConflict, cli.ExitCheckFailed}
	var got []int
	for _, e := range helpExitCodes {
		got = append(got, e.Code)
	}
	sort.Ints(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("help exit codes = %v, want %v", got, want)
	}
}

// What the overview calls "not implemented yet" must really be missing, so
// the list is removed from the help the day a command arrives.
func TestHelpPlannedCommandsAreMissing(t *testing.T) {
	for _, p := range helpPlanned {
		if _, ok := commands[p]; ok {
			t.Errorf("%q is implemented but still listed as planned", p)
		}
		if findHelpTopic(p) != nil {
			t.Errorf("%q has a help topic but is listed as planned", p)
		}
	}
}

func TestDispatchHelpWordIsNotACommandOfTheTable(t *testing.T) {
	if _, ok := commands["help"]; ok {
		t.Error("help must be answered by parseHelpRequest, not by the command table")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var (
	helpBraced      = regexp.MustCompile(`\{[^}]*\}`)
	helpBareUpper   = regexp.MustCompile(`[A-Z]{2,}`)
	helpAngleHolder = regexp.MustCompile(`<[A-Za-z]`)
)

// A placeholder in a usage line, a flag or an example is written in curly
// braces ({ACCOUNT}), never as <name> or as a bare upper-case word.
func TestHelpPlaceholdersAreBraced(t *testing.T) {
	angle := func(where, text string) {
		if helpAngleHolder.MatchString(text) {
			t.Errorf("%s: %q uses <angle brackets>; write the placeholder as {NAME}", where, text)
		}
	}
	// Examples and the quick start hold real values (DE, NL), so only usage lines
	// and flag names are checked for bare upper-case words.
	check := func(where, text string) {
		angle(where, text)
		if bare := helpBareUpper.FindString(helpBraced.ReplaceAllString(text, "")); bare != "" {
			t.Errorf("%s: %q has a placeholder %q outside curly braces", where, text, bare)
		}
	}
	for _, tp := range helpTopics {
		for _, u := range tp.Usage {
			check(tp.Name+" usage", u)
		}
		for _, f := range tp.Flags {
			check(tp.Name+" flag", f.Name)
		}
		for _, e := range tp.Examples {
			angle(tp.Name+" example", e.Command)
		}
	}
	for _, u := range helpOverviewUsage {
		check("overview usage", u)
	}
	for _, f := range helpGlobalFlags {
		check("global flag", f.Name)
	}
	for _, q := range helpQuickStart {
		angle("quick start", q)
	}
}

// conceptText joins the free-text fields of a topic into one string, so a
// test can assert on what the rendered help will say.
func conceptText(tp *helpTopic) string {
	var b strings.Builder
	b.WriteString(tp.Summary + "\n" + tp.Description)
	for _, f := range tp.Flags {
		b.WriteString("\n" + f.Name + " " + f.Meaning)
	}
	for _, e := range tp.Examples {
		b.WriteString("\n" + e.Comment + "\n" + e.Command)
	}
	for _, n := range tp.Notes {
		b.WriteString("\n" + n)
	}
	return b.String()
}

// Every key the configuration loader accepts (internal/config/config.go,
// allowed_clients.go and load.go; documented in docs/config.md) must be
// named by the config topic, so the binary alone explains its own file.
func TestSOCKS5ChainHelpCoverage(t *testing.T) {
	for _, name := range []string{"account set-socks5", "account show", "config", "routing"} {
		if findHelpTopic(name) == nil {
			t.Errorf("no help topic for %q", name)
		}
	}
	add := renderTopic(findHelpTopic("account add"))
	for _, flag := range []string{"--pre-socks5", "--post-socks5"} {
		if !strings.Contains(add, flag) {
			t.Errorf("account add topic lacks %q", flag)
		}
	}
	set := renderTopic(findHelpTopic("account set-socks5"))
	for _, want := range []string{"--pre", "--post", "inherit", "none", "socks5://", "port required", "netivri help"} {
		if !strings.Contains(set, want) {
			t.Errorf("account set-socks5 topic lacks %q", want)
		}
	}
	show := renderTopic(findHelpTopic("account show"))
	for _, want := range []string{"pre_socks5", "post_socks5", "redacted"} {
		if !strings.Contains(show, want) {
			t.Errorf("account show topic lacks %q", want)
		}
	}
	for _, name := range []string{"account set-socks5", "account show"} {
		out := mustDispatch(t, "help", name)
		if !strings.HasPrefix(out, "netivri "+name+" - ") {
			t.Errorf("help %s starts with %q", name, firstLine(out))
		}
	}
}

func TestConfigTopicNamesEveryConfigKey(t *testing.T) {
	keys := []string{
		"chain",
		"chain.pre_socks5",
		"chain.post_socks5",

		"listen.bind",
		"listen.port",
		"listen.allowed_clients",
		"auth.login",
		"auth.password",
		"auth.allow_init_remote",
		"fetch.proxy",
		"fetch.update_interval",
		"subscriptions",
		"subscriptions.name",
		"subscriptions.url",
		"subscriptions.via",
		"accounts",
		"accounts.login",
		"accounts.password",
		"accounts.provider",
		"accounts.exit_country",
		"accounts.allowed_clients",
		"accounts.pre_socks5",
		"accounts.post_socks5",
		"selection.node",
		"selection.country",
		"selection.mode",
	}
	tp := findHelpTopic("config")
	if tp == nil {
		t.Fatal("no help topic for config")
	}
	text := conceptText(tp)
	var missing []string
	for _, k := range keys {
		if !strings.Contains(text, k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the config topic does not name these keys: %s", strings.Join(missing, ", "))
	}
}

func TestConceptTopicsAreListed(t *testing.T) {
	overview := renderOverview()
	if !strings.Contains(overview, "Concepts") {
		t.Error("the overview has no Concepts group")
	}
	for _, name := range conceptTopics {
		tp := findHelpTopic(name)
		if tp == nil {
			t.Fatalf("no help topic for %q", name)
		}
		if tp.Group != "Concepts" {
			t.Errorf("topic %q has group %q, want Concepts", name, tp.Group)
		}
		out := mustDispatch(t, "help", name)
		if !strings.HasPrefix(out, "netivri "+name+" - ") {
			t.Errorf("help %s starts with %q", name, firstLine(out))
		}
	}
	help := decodeHelp(t, mustDispatch(t, "help", "--json"))
	cmds, _ := help["commands"].([]any)
	names := map[string]bool{}
	for _, c := range cmds {
		names[c.(map[string]any)["name"].(string)] = true
	}
	for _, name := range conceptTopics {
		if !names[name] {
			t.Errorf("help --json overview lacks the concept topic %q", name)
		}
	}
}

// The concept topics are not commands, but help NAME --json must give the
// same machine-readable document as any other topic.
func TestConceptTopicsJSONForms(t *testing.T) {
	for _, name := range conceptTopics {
		tp := findHelpTopic(name)
		if tp == nil {
			t.Fatalf("no help topic for %q", name)
		}
		help := decodeHelp(t, mustDispatch(t, "help", name, "--json"))
		if help["kind"] != "topic" {
			t.Errorf("help %s --json: kind = %v, want topic", name, help["kind"])
		}
		if help["name"] != tp.Name {
			t.Errorf("help %s --json: name = %v, want %q", name, help["name"], tp.Name)
		}
		if help["summary"] != tp.Summary {
			t.Errorf("help %s --json: summary = %v, want %q", name, help["summary"], tp.Summary)
		}
	}
}

// docRefPrefix turns a repository path in the docs into a URL that works
// for someone with only the binary.
const docRefPrefix = "https://github.com/PHPCraftdream/netivri/blob/main/"

// noBareDocsRef fails when s names a file under docs/ by path instead of
// the full URL: after cutting the URL-prefixed references away, no
// "docs/" may remain.
func noBareDocsRef(t *testing.T, where, s string) {
	t.Helper()
	stripped := strings.ReplaceAll(s, docRefPrefix+"docs", "")
	if strings.Contains(stripped, "docs/") {
		t.Errorf("%s: help text names docs/ by path instead of the full URL: %q", where, s)
	}
}

func TestHelpUsesAbsoluteDocURLs(t *testing.T) {
	for i, line := range strings.Split(renderOverview(), "\n") {
		noBareDocsRef(t, "overview:"+strconv.Itoa(i+1), line)
	}
	for i := range helpTopics {
		tp := &helpTopics[i]
		where := tp.Name
		noBareDocsRef(t, where+" summary", tp.Summary)
		for _, u := range tp.Usage {
			noBareDocsRef(t, where+" usage", u)
		}
		noBareDocsRef(t, where+" description", tp.Description)
		for _, f := range tp.Flags {
			noBareDocsRef(t, where+" flag", f.Name)
			noBareDocsRef(t, where+" flag meaning", f.Meaning)
		}
		for _, e := range tp.Examples {
			noBareDocsRef(t, where+" example comment", e.Comment)
			noBareDocsRef(t, where+" example command", e.Command)
		}
		for _, e := range tp.Errors {
			noBareDocsRef(t, where+" error meaning", e.Meaning)
		}
		for _, n := range tp.Notes {
			noBareDocsRef(t, where+" note", n)
		}
		for _, s := range tp.SeeAlso {
			noBareDocsRef(t, where+" see also", s)
		}
	}
}

// Facts that were once misdescribed in the concept topics; a regression here
// would mislead an agent that has only the binary.
func TestConceptTopicsKeepCorrectedFacts(t *testing.T) {
	text := func(name string) string {
		tp := findHelpTopic(name)
		if tp == nil {
			t.Fatalf("no help topic for %q", name)
		}
		return renderTopic(tp)
	}
	routing := text("routing")
	for _, want := range []string{"2 consecutive failed probes", "3 consecutive failed dials", "client -> pre-hop -> selected exit -> post-hop -> target", "port", "inherit", "none", "redacted", "netivri help"} {
		if !strings.Contains(strings.Join(strings.Fields(routing), " "), want) {
			t.Errorf("routing topic lacks %q", want)
		}
	}
	output := strings.Join(strings.Fields(text("output")), " ")
	for _, bad := range []string{"password_set", "account (login"} {
		if strings.Contains(output, bad) {
			t.Errorf("output topic still has %q, which account show does not print", bad)
		}
	}
	if strings.Contains(output, "internal proxy name -, type") {
		t.Error("node list name is the provider's node name, not the internal proxy name")
	}
}
