package tui

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type PromptKind int

const (
	TextPrompt PromptKind = iota
	SecretPrompt
	NumberPrompt
	ChoicePrompt
	MultiChoicePrompt
	YesNoPrompt
	ConfirmPrompt
)

type PromptOption struct{ Label, Value, Description string }
type PromptResultMsg struct {
	Value    string
	Selected []string
	Canceled bool
}
type Prompt struct {
	Kind                                                  PromptKind
	Theme                                                 Theme
	TitleText, Label, Hint, Value                         string
	Options                                               []PromptOption
	Selected                                              []string
	Min, Max                                              *int
	Validate                                              func(string) error
	AllowReveal, Saving                                   bool
	Error                                                 error
	Buttons                                               Buttons
	ConfirmLabel                                          string
	input                                                 textinput.Model
	initialized, changed, reveal, discard, buttonsFocused bool
	choice, optionOffset, buttonY                         int
	width, height                                         int
}

func NewPrompt(kind PromptKind, title, label, hint, value string) *Prompt {
	in := textinput.New()
	in.SetValue(value)
	in.Focus()
	labels := []string{"Save", "Cancel"}
	if kind == ConfirmPrompt {
		labels = []string{"Cancel", "Remove"}
	}
	return &Prompt{Kind: kind, TitleText: title, Label: label, Hint: hint, Value: value, input: in, Buttons: Buttons{Labels: labels, Focus: 0}, buttonsFocused: kind == ConfirmPrompt, ConfirmLabel: "Remove"}
}
func (p *Prompt) SetTheme(t Theme)     { p.Theme = t }
func (p *Prompt) Title() string        { return p.TitleText }
func (p *Prompt) CapturingInput() bool { return true }
func (p *Prompt) SetError(e error)     { p.Error = e }
func (p *Prompt) SetSaving(v bool)     { p.Saving = v }
func (p *Prompt) Init() tea.Cmd        { p.initialize(); return nil }
func (p *Prompt) initialize() {
	if p.initialized {
		return
	}
	p.initialized = true
	if p.Kind == ChoicePrompt && len(p.Options) > 0 {
		p.choice = 0
		for i, o := range p.Options {
			if o.Value == p.Value {
				p.choice = i
				break
			}
		}
		p.Value = p.Options[p.choice].Value
	} else if p.Kind == MultiChoicePrompt {
		p.Value = strings.Join(p.Selected, ",")
	}
	if p.Kind == TextPrompt || p.Kind == SecretPrompt || p.Kind == NumberPrompt {
		if p.input.Value() != p.Value {
			p.input.SetValue(p.Value)
		}
	}
}
func (p *Prompt) GetAnswer() (string, error) {
	p.initialize()
	if p.Kind == ChoicePrompt {
		if len(p.Options) == 0 {
			return "", fmt.Errorf("Choose an option")
		}
		if p.choice < 0 || p.choice >= len(p.Options) {
			p.choice = 0
		}
		return p.Options[p.choice].Value, nil
	}
	if p.Kind == MultiChoicePrompt {
		return strings.Join(p.Selected, ","), nil
	}
	return p.Value, nil
}
func (p *Prompt) Update(msg tea.Msg) (Modal, tea.Cmd) {
	p.initialize()
	if s, ok := msg.(tea.WindowSizeMsg); ok {
		p.width, p.height = s.Width, s.Height
		return p, nil
	}
	if p.Saving {
		return p, nil
	}
	if m, ok := msg.(tea.MouseClickMsg); ok {
		if m.Button != tea.MouseLeft {
			return p, nil
		}
		if p.discard {
			if i, hit := p.Buttons.Update(msg, p.buttonY); hit && i >= 0 {
				if i == 0 {
					p.resume()
					return p, nil
				}
				return p, p.result(true)
			}
		}
		if m.Y == p.buttonY {
			if i, hit := p.Buttons.Update(msg, p.buttonY); hit && i >= 0 {
				if p.discard {
					return p, p.selectButton(i)
				}
				if p.Kind == ConfirmPrompt {
					return p, p.confirmButton(i)
				}
				return p, p.selectButton(i)
			}
		}
		if p.Kind == ChoicePrompt || p.Kind == MultiChoicePrompt {
			slots := max(0, p.height-7)
			i := p.optionOffset + m.Y - 3
			if m.Y >= 3 && m.Y < 3+slots && i >= 0 && i < len(p.Options) {
				p.choice = i
				if p.Kind == MultiChoicePrompt {
					p.toggle()
				}
			}
		}
		if p.Kind == TextPrompt || p.Kind == SecretPrompt || p.Kind == NumberPrompt {
			if m.Y == 3 {
				p.input.Focus()
			}
		}
		return p, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	key := k.String()
	if p.discard {
		switch key {
		case "esc":
			p.resume()
			return p, nil
		case "left", "right", "tab", "shift+tab":
			if key == "left" || key == "shift+tab" {
				p.Buttons.Focus = 0
			} else {
				p.Buttons.Focus = 1
			}
			return p, nil
		case "enter":
			if p.Buttons.Focus == 0 {
				p.resume()
				return p, nil
			}
			return p, p.result(true)
		}
		return p, nil
	}
	if key == "esc" {
		if p.changed {
			p.discard = true
			p.Buttons = Buttons{Labels: []string{"Continue", "Discard"}}
			p.Buttons.Focus = 0
			return p, nil
		}
		p.reveal = false
		return p, p.result(true)
	}
	if p.Kind == ConfirmPrompt {
		switch key {
		case "y", "Y":
			return p, p.confirmButton(1)
		case "n", "N":
			return p, p.confirmButton(0)
		case "left", "right":
			p.buttonsFocused = true
			d := 1
			if key == "left" {
				d = -1
			}
			p.Buttons.Move(d)
			return p, nil
		case "tab", "shift+tab":
			p.buttonsFocused = true
			d := 1
			if key == "shift+tab" {
				d = -1
			}
			p.Buttons.Move(d)
			return p, nil
		case "enter":
			return p, p.confirmButton(p.Buttons.Focus)
		}
		return p, nil
	}
	if key == "tab" || key == "shift+tab" {
		if !p.buttonsFocused {
			p.buttonsFocused = true
			if key == "tab" {
				p.Buttons.Focus = 0
			} else {
				p.Buttons.Focus = len(p.Buttons.Labels) - 1
			}
		} else {
			d := 1
			if key == "shift+tab" {
				d = -1
			}
			p.Buttons.Move(d)
			if (key == "shift+tab" && p.Buttons.Focus == len(p.Buttons.Labels)-1) || (key == "tab" && p.Buttons.Focus == 0) {
				p.buttonsFocused = false
			}
		}
		return p, nil
	}
	if key == "ctrl+r" && p.Kind == SecretPrompt && p.AllowReveal {
		p.reveal = !p.reveal
		return p, nil
	}
	if p.buttonsFocused {
		if key == "left" || key == "right" {
			p.buttonsFocused = true
			d := 1
			if key == "left" {
				d = -1
			}
			p.Buttons.Move(d)
			return p, nil
		}
		if key == "enter" {
			if p.Kind == ConfirmPrompt {
				return p, p.confirmButton(p.Buttons.Focus)
			}
			return p, p.selectButton(p.Buttons.Focus)
		}
	}
	if key == "enter" {
		if p.Kind == YesNoPrompt {
			if p.Value == "" {
				p.Value = "No"
			}
			return p, p.accept()
		}
		return p, p.accept()
	}
	if key == "up" || key == "down" {
		if p.Kind == ChoicePrompt || p.Kind == MultiChoicePrompt {
			p.moveChoice(key == "down")
			return p, nil
		}
		if p.Kind == YesNoPrompt {
			p.Value = map[bool]string{true: "Yes", false: "No"}[key == "up"]
			p.changed = true
			return p, nil
		}
	}
	if key == " " && p.Kind == MultiChoicePrompt {
		p.toggle()
		return p, nil
	}
	if (key == "y" || key == "n") && p.Kind == YesNoPrompt {
		p.Value = map[bool]string{true: "Yes", false: "No"}[key == "y"]
		p.changed = true
		return p, nil
	}
	if p.Kind == ChoicePrompt || p.Kind == MultiChoicePrompt || p.Kind == YesNoPrompt {
		return p, nil
	}
	old := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.Value = p.input.Value()
	if old != p.Value {
		p.changed = true
		p.Error = nil
	}
	return p, cmd
}
func (p *Prompt) resume() {
	p.discard = false
	p.Buttons = Buttons{Labels: []string{"Save", "Cancel"}}
	p.buttonsFocused = false
	p.reveal = false
}
func (p *Prompt) selectButton(i int) tea.Cmd {
	if i == 1 {
		return p.result(true)
	}
	return p.accept()
}
func (p *Prompt) confirmButton(i int) tea.Cmd {
	if i == 0 {
		return p.result(true)
	}
	return p.result(false)
}
func (p *Prompt) moveChoice(down bool) {
	if len(p.Options) == 0 {
		return
	}
	if down {
		p.choice = (p.choice + 1) % len(p.Options)
	} else {
		p.choice = (p.choice + len(p.Options) - 1) % len(p.Options)
	}
}
func (p *Prompt) toggle() {
	if len(p.Options) == 0 {
		return
	}
	v := p.Options[p.choice].Value
	for i, s := range p.Selected {
		if s == v {
			p.Selected = append(p.Selected[:i], p.Selected[i+1:]...)
			return
		}
	}
	p.Selected = append(p.Selected, v)
}
func (p *Prompt) accept() tea.Cmd {
	p.Error = nil
	var e error
	if p.Kind == ChoicePrompt || p.Kind == MultiChoicePrompt {
		var v string
		v, e = p.GetAnswer()
		if e == nil {
			p.Value = v
		}
	}
	if p.Kind == YesNoPrompt && p.Value == "" {
		p.Value = "No"
	}
	if p.Kind == NumberPrompt {
		n, x := strconv.Atoi(p.Value)
		if p.Value == "" || x != nil {
			e = fmt.Errorf("Enter a whole number")
		} else if p.Min != nil && n < *p.Min {
			e = fmt.Errorf("Must be at least %d", *p.Min)
		} else if p.Max != nil && n > *p.Max {
			e = fmt.Errorf("Must be at most %d", *p.Max)
		}
	}
	if e == nil && p.Validate != nil {
		e = p.Validate(p.Value)
	}
	if e != nil {
		p.Error = fmt.Errorf("%s", p.safeError(e.Error()))
		return nil
	}
	return p.result(false)
}
func (p *Prompt) safeError(s string) string {
	secrets := []string{p.Value}
	if u, e := url.Parse(p.Value); e == nil {
		if u.User != nil {
			if v := u.User.Username(); v != "" {
				secrets = append(secrets, v)
			}
			if v, ok := u.User.Password(); ok && v != "" {
				secrets = append(secrets, v)
			}
		}
		for _, v := range []string{u.Host, u.Path, u.RawQuery} {
			if v != "" {
				secrets = append(secrets, v)
			}
		}
		for _, pair := range strings.Split(u.RawQuery, "&") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 && kv[1] != "" {
				decoded, e := url.QueryUnescape(kv[1])
				if e == nil {
					secrets = append(secrets, decoded)
				}
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
func (p *Prompt) result(cancel bool) tea.Cmd {
	v := p.Value
	if cancel {
		v = ""
		p.reveal = false
	}
	sel := append([]string(nil), p.Selected...)
	return func() tea.Msg { return PromptResultMsg{Value: v, Selected: sel, Canceled: cancel} }
}
func (p *Prompt) View(w, h int) string {
	p.initialize()
	p.width, p.height = w, h
	p.buttonY = h - 1
	if h <= 0 {
		return ""
	}
	rows := make([]string, h)
	put := func(y int, s string) {
		if y >= 0 && y < h {
			rows[y] = s
		}
	}
	t := p.Theme
	if t.Accent == nil {
		t = NewTheme()
	}
	put(0, p.TitleText)
	put(1, p.Hint)
	if p.Kind == ChoicePrompt || p.Kind == MultiChoicePrompt {
		slots := max(0, h-7)
		if p.choice < p.optionOffset {
			p.optionOffset = p.choice
		}
		if slots > 0 && p.choice >= p.optionOffset+slots {
			p.optionOffset = p.choice - slots + 1
		}
		for n := 0; n < slots && p.optionOffset+n < len(p.Options); n++ {
			i := p.optionOffset + n
			o := p.Options[i]
			mark := " "
			if i == p.choice {
				mark = t.FocusMark()
			}
			check := ""
			if p.Kind == MultiChoicePrompt {
				check = "[ ] "
				for _, v := range p.Selected {
					if v == o.Value {
						check = "[x] "
						break
					}
				}
			}
			line := mark + " " + check + o.Label
			if o.Description != "" {
				sep := " — "
				if lipgloss.Width(sep) != 3 {
					sep = " - "
				}
				line += sep + o.Description
			}
			put(3+n, line)
		}
	} else if p.Kind == YesNoPrompt {
		put(3, p.Value+" (Yes / No)")
	} else if p.Kind == ConfirmPrompt {
		put(3, p.Label)
	} else {
		if p.Kind == SecretPrompt && !p.reveal {
			p.input.EchoMode = textinput.EchoPassword
			p.input.EchoCharacter = '*'
		} else {
			p.input.EchoMode = textinput.EchoNormal
		}
		p.input.SetWidth(max(1, w-lipgloss.Width(p.Label)-4))
		label := p.Label + "  " + p.input.View()
		if p.Kind == SecretPrompt && p.reveal {
			put(2, "Warning: secret visible (Ctrl+R to hide)")
		}
		put(3, label)
	}
	if p.discard {
		put(h-3, "Discard changes? Continue editing or discard")
	} else if p.Error != nil {
		put(h-3, "Error: "+p.safeError(p.Error.Error()))
	}
	if p.Saving {
		put(h-2, "Saving...")
	}
	buttons := p.Buttons
	if !p.buttonsFocused && !p.discard {
		buttons.Focus = -1
	}
	put(h-1, buttons.View(t))
	return boundLines(rows, w, h)
}

var _ = lipgloss.Width
