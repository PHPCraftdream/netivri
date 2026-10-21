package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// helpWidth is the column where wrapped help text ends.
const helpWidth = 78

// helpMaxLabel caps the label column of two-column tables; a longer label
// gets a line of its own.
const helpMaxLabel = 30

// helpTopic is one help screen. The same data is printed as text for people
// and as JSON for scripts and agents (`netivri help --json`).
type helpTopic struct {
	Name        string        `json:"name"`
	Group       string        `json:"group,omitempty"`
	Summary     string        `json:"summary"`
	Usage       []string      `json:"usage"`
	Description string        `json:"description,omitempty"`
	Flags       []helpFlag    `json:"flags,omitempty"`
	Examples    []helpExample `json:"examples,omitempty"`
	Errors      []helpError   `json:"errors,omitempty"`
	Notes       []string      `json:"notes,omitempty"`
	SeeAlso     []string      `json:"see_also,omitempty"`
}

type helpFlag struct {
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}

type helpExample struct {
	Comment string `json:"comment"`
	Command string `json:"command"`
}

type helpError struct {
	Code    string `json:"code"`
	Exit    int    `json:"exit"`
	Meaning string `json:"meaning"`
}

// helpCommand is one row of a command list.
type helpCommand struct {
	Name    string   `json:"name"`
	Group   string   `json:"group,omitempty"`
	Summary string   `json:"summary"`
	Usage   []string `json:"usage,omitempty"`
}

type helpExit struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

type helpTopicJSON struct {
	Kind string `json:"kind"`
	helpTopic
	Subcommands []helpCommand `json:"subcommands,omitempty"`
}

type helpOverviewJSON struct {
	Kind        string        `json:"kind"`
	Summary     string        `json:"summary"`
	Description string        `json:"description"`
	Usage       []string      `json:"usage"`
	QuickStart  []string      `json:"quick_start"`
	Commands    []helpCommand `json:"commands"`
	GlobalFlags []helpFlag    `json:"global_flags"`
	ExitCodes   []helpExit    `json:"exit_codes"`
	Planned     []string      `json:"planned"`
}

// helpRequest is what the arguments ask of the help system.
type helpRequest struct {
	topic   []string
	json    bool
	badFlag string
}

func isJSONFlag(a string) bool { return a == "--json" || a == "-json" }

func isHelpFlag(a string) bool { return a == "-h" || a == "--help" || a == "-help" }

// parseHelpRequest recognises every way to ask for help: no arguments, the
// word `help`, a help flag in the first position, or a help flag after a
// command (`netivri sub add --help`). A leading --json selects the JSON form.
// ok is false for everything else, which is dispatched as a command.
func parseHelpRequest(args []string) (helpRequest, bool) {
	var req helpRequest
	rest := args
	for len(rest) > 0 && isJSONFlag(rest[0]) {
		req.json = true
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return req, true
	}

	if rest[0] == "help" || isHelpFlag(rest[0]) {
		for _, a := range rest[1:] {
			switch {
			case isJSONFlag(a), isHelpFlag(a):
				req.json = req.json || isJSONFlag(a)
			case a == "--quiet" || a == "-quiet" || a == "--no-color" || a == "-no-color":
			case strings.HasPrefix(a, "-"):
				if req.badFlag == "" {
					req.badFlag = a
				}
			default:
				req.topic = append(req.topic, a)
			}
		}
		return req, true
	}

	if _, known := commands[rest[0]]; !known {
		return req, false
	}
	asked := false
	for _, a := range rest[1:] {
		if a == "--" {
			break
		}
		if isHelpFlag(a) {
			asked = true
		}
	}
	if !asked {
		return req, false
	}
	for _, a := range rest[1:] {
		if isJSONFlag(a) {
			req.json = true
		}
	}
	req.topic = []string{rest[0]}
	if len(rest) > 1 && !strings.HasPrefix(rest[1], "-") {
		if findHelpTopic(rest[0]+" "+rest[1]) != nil {
			req.topic = append(req.topic, rest[1])
		}
	}
	return req, true
}

// runHelp prints the overview or one topic, as text unless JSON was asked.
func runHelp(req helpRequest, stdout io.Writer) error {
	if req.badFlag != "" {
		e := cli.Usage("help: unknown flag %s", req.badFlag)
		e.Hint = "run: netivri help"
		return e
	}
	var topic *helpTopic
	if len(req.topic) > 0 {
		name := strings.Join(req.topic, " ")
		topic = findHelpTopic(name)
		if topic == nil {
			e := cli.Usage("help: no help topic %q", name)
			e.Hint = "run: netivri help"
			return e
		}
	}
	switch {
	case req.json && topic == nil:
		cli.Success(stdout, map[string]any{"help": overviewJSON()})
	case req.json:
		cli.Success(stdout, map[string]any{"help": topicJSON(topic)})
	case topic == nil:
		_, _ = io.WriteString(stdout, renderOverview())
	default:
		_, _ = io.WriteString(stdout, renderTopic(topic))
	}
	return nil
}

func findHelpTopic(name string) *helpTopic {
	for i := range helpTopics {
		if helpTopics[i].Name == name {
			return &helpTopics[i]
		}
	}
	return nil
}

// subTopics returns the topics named "<parent> <something>".
func subTopics(parent string) []helpTopic {
	var out []helpTopic
	for _, t := range helpTopics {
		if strings.HasPrefix(t.Name, parent+" ") {
			out = append(out, t)
		}
	}
	return out
}

func topicJSON(t *helpTopic) helpTopicJSON {
	out := helpTopicJSON{Kind: "topic", helpTopic: *t}
	for _, s := range subTopics(t.Name) {
		out.Subcommands = append(out.Subcommands, helpCommand{Name: s.Name, Summary: s.Summary, Usage: s.Usage})
	}
	return out
}

// listedCommands returns the commands shown in the overview, in help order.
func listedCommands() []helpCommand {
	var out []helpCommand
	for _, g := range helpGroups {
		for _, t := range helpTopics {
			if t.Group == g {
				out = append(out, helpCommand{Name: t.Name, Group: g, Summary: t.Summary, Usage: t.Usage})
			}
		}
	}
	return out
}

func overviewJSON() helpOverviewJSON {
	return helpOverviewJSON{
		Kind:        "overview",
		Summary:     helpSummary,
		Description: helpIntro,
		Usage:       helpOverviewUsage,
		QuickStart:  helpQuickStart,
		Commands:    listedCommands(),
		GlobalFlags: helpGlobalFlags,
		ExitCodes:   helpExitCodes,
		Planned:     helpPlanned,
	}
}

func renderOverview() string {
	var b strings.Builder
	b.WriteString("netivri - " + helpSummary + "\n\n")
	for _, l := range wrap(helpIntro, 0, helpWidth) {
		b.WriteString(l + "\n")
	}
	section(&b, "USAGE")
	for _, u := range helpOverviewUsage {
		b.WriteString("  " + u + "\n")
	}
	section(&b, "QUICK START")
	for _, q := range helpQuickStart {
		b.WriteString("  " + q + "\n")
	}
	section(&b, "COMMANDS")
	cmds := listedCommands()
	for _, g := range helpGroups {
		b.WriteString("  " + g + "\n")
		var rows [][2]string
		for _, c := range cmds {
			if c.Group == g {
				rows = append(rows, [2]string{c.Name, c.Summary})
			}
		}
		writeRows(&b, rows, 4, helpWidth)
	}
	b.WriteString("\n  Run `netivri help <command>` for the flags and examples of one command,\n")
	b.WriteString("  for example `netivri help sub add` or `netivri help up`.\n")
	section(&b, "GLOBAL FLAGS")
	rows := make([][2]string, len(helpGlobalFlags))
	for i, f := range helpGlobalFlags {
		rows[i] = [2]string{f.Name, f.Meaning}
	}
	writeRows(&b, rows, 2, helpWidth)
	section(&b, "OUTPUT")
	for _, l := range wrap(helpOutput, 2, helpWidth) {
		b.WriteString(l + "\n")
	}
	section(&b, "EXIT CODES")
	rows = rows[:0]
	for _, e := range helpExitCodes {
		rows = append(rows, [2]string{fmt.Sprint(e.Code), e.Meaning})
	}
	writeRows(&b, rows, 2, helpWidth)
	section(&b, "NOT IMPLEMENTED YET")
	for _, l := range wrap("Described in docs/cli.md, but not available yet: "+strings.Join(helpPlanned, ", ")+".", 2, helpWidth) {
		b.WriteString(l + "\n")
	}
	section(&b, "MORE")
	b.WriteString("  CLI contract:   docs/cli.md\n")
	b.WriteString("  Project:        https://github.com/PHPCraftdream/netivri\n")
	return b.String()
}

func renderTopic(t *helpTopic) string {
	var b strings.Builder
	b.WriteString("netivri " + t.Name + " - " + t.Summary + "\n")
	section(&b, "USAGE")
	for _, u := range t.Usage {
		for _, l := range usageLines(u) {
			b.WriteString(l + "\n")
		}
	}
	if t.Description != "" {
		section(&b, "DESCRIPTION")
		for _, l := range wrap(t.Description, 2, helpWidth) {
			b.WriteString(l + "\n")
		}
	}
	if subs := subTopics(t.Name); len(subs) > 0 {
		section(&b, "COMMANDS")
		rows := make([][2]string, len(subs))
		for i, s := range subs {
			rows[i] = [2]string{s.Name, s.Summary}
		}
		writeRows(&b, rows, 2, helpWidth)
		b.WriteString("\n  Run `netivri help " + t.Name + " <command>` for the details of one command.\n")
	}
	if len(t.Flags) > 0 {
		section(&b, "FLAGS")
		rows := make([][2]string, len(t.Flags))
		for i, f := range t.Flags {
			rows[i] = [2]string{f.Name, f.Meaning}
		}
		writeRows(&b, rows, 2, helpWidth)
	}
	if len(t.Examples) > 0 {
		section(&b, "EXAMPLES")
		for i, e := range t.Examples {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, l := range wrap(e.Comment, 4, helpWidth) {
				b.WriteString("  # " + strings.TrimLeft(l, " ") + "\n")
			}
			for _, c := range strings.Split(e.Command, "\n") {
				b.WriteString("  " + c + "\n")
			}
		}
	}
	if len(t.Errors) > 0 {
		section(&b, "ERRORS")
		rows := make([][2]string, len(t.Errors))
		for i, e := range t.Errors {
			rows[i] = [2]string{fmt.Sprintf("%s (exit %d)", e.Code, e.Exit), e.Meaning}
		}
		writeRows(&b, rows, 2, helpWidth)
	}
	if len(t.Notes) > 0 {
		section(&b, "NOTES")
		for i, n := range t.Notes {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, l := range wrap(n, 2, helpWidth) {
				b.WriteString(l + "\n")
			}
		}
	}
	if len(t.SeeAlso) > 0 {
		section(&b, "SEE ALSO")
		for _, s := range t.SeeAlso {
			b.WriteString("  netivri help " + s + "\n")
		}
	}
	return b.String()
}

// usageLines folds a long usage line and indents its continuation.
func usageLines(u string) []string {
	lines := wrap(u, 2, helpWidth-4)
	for i := 1; i < len(lines); i++ {
		lines[i] = "    " + lines[i]
	}
	return lines
}

func section(b *strings.Builder, title string) {
	b.WriteString("\n" + title + "\n")
}

// wrap folds text at width columns and indents every line by indent spaces.
// A blank line in text starts a new paragraph.
func wrap(text string, indent, width int) []string {
	pad := strings.Repeat(" ", indent)
	var lines []string
	for i, para := range strings.Split(text, "\n\n") {
		if i > 0 {
			lines = append(lines, "")
		}
		cur := ""
		for _, w := range strings.Fields(para) {
			switch {
			case cur == "":
				cur = w
			case indent+len(cur)+1+len(w) > width:
				lines = append(lines, pad+cur)
				cur = w
			default:
				cur += " " + w
			}
		}
		if cur != "" {
			lines = append(lines, pad+cur)
		}
	}
	return lines
}

// writeRows prints a two-column table with a hanging indent. A label wider
// than the column goes on its own line, followed by the meaning.
func writeRows(b *strings.Builder, rows [][2]string, indent, width int) {
	labelW := 0
	for _, r := range rows {
		if n := len(r[0]); n > labelW && n <= helpMaxLabel {
			labelW = n
		}
	}
	pad := strings.Repeat(" ", indent)
	hang := indent + labelW + 2
	for _, r := range rows {
		lines := wrap(r[1], hang, width)
		if len(r[0]) > labelW || len(lines) == 0 {
			b.WriteString(pad + r[0] + "\n")
			for _, l := range lines {
				b.WriteString(l + "\n")
			}
			continue
		}
		first := strings.TrimLeft(lines[0], " ")
		b.WriteString(pad + r[0] + strings.Repeat(" ", labelW-len(r[0])+2) + first + "\n")
		for _, l := range lines[1:] {
			b.WriteString(l + "\n")
		}
	}
}
