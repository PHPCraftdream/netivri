package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type FieldKind int

const (
	TextField FieldKind = iota
	PasswordField
	SingleChoiceField
	MultiChoiceField
	YesNoField
)

type Field struct {
	Name     string
	Kind     FieldKind
	Value    string
	Options  []string
	Validate func(string) error
	Selected []string
	Error    string
}
type FormResultMsg struct {
	Values   map[string]string
	Err      error
	Canceled bool
}

// Form describes validated input fields and implements Screen. Use AsModal for frame modals.
type Form struct {
	TitleText string
	Fields    []Field
	Focus     int
	Submit    func(map[string]string) error
	Cancel    func()
	err       error
	scroll    int
	width     int
	height    int
}

func (f *Form) Title() string        { return f.TitleText }
func (f *Form) CapturingInput() bool { return true }
func (f *Form) KeyHints() string {
	return "Tab/Shift-Tab fields  Enter next/submit  Esc cancel  Arrows/Space choices"
}
func (f *Form) Init() tea.Cmd { return nil }
func (f *Form) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		f.width, f.height = max(0, size.Width), max(0, size.Height)
		f.layout(f.width, f.height)
		return f, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return f, nil
	}
	switch k.String() {
	case "esc":
		if f.Cancel != nil {
			f.Cancel()
		}
		return f, func() tea.Msg { return FormResultMsg{Canceled: true} }
	case "tab", "enter":
		if len(f.Fields) == 0 || f.Focus >= len(f.Fields)-1 {
			return f, f.submit()
		}
		f.Focus++
		f.layout(f.width, f.height)
		return f, nil
	case "shift+tab":
		if f.Focus > 0 {
			f.Focus--
		}
		f.layout(f.width, f.height)
		return f, nil
	case "backspace":
		if f.Focus < len(f.Fields) && (f.Fields[f.Focus].Kind == TextField || f.Fields[f.Focus].Kind == PasswordField) {
			r := []rune(f.Fields[f.Focus].Value)
			if len(r) > 0 {
				f.Fields[f.Focus].Value = string(r[:len(r)-1])
			}
		}
		return f, nil
	case "up", "down":
		if f.Focus < len(f.Fields) {
			field := &f.Fields[f.Focus]
			switch field.Kind {
			case SingleChoiceField:
				if len(field.Options) > 0 {
					n := optionIndex(field.Options, field.Value)
					if k.String() == "up" {
						n = (n + len(field.Options) - 1) % len(field.Options)
					} else {
						n = (n + 1) % len(field.Options)
					}
					field.Value = field.Options[n]
				}
			case MultiChoiceField:
				if len(field.Options) > 0 {
					n := optionIndex(field.Options, field.Value)
					if k.String() == "up" {
						n = (n + len(field.Options) - 1) % len(field.Options)
					} else {
						n = (n + 1) % len(field.Options)
					}
					field.Value = field.Options[n]
				}
			}
		}
		return f, nil
	case "space", " ":
		if f.Focus < len(f.Fields) {
			field := &f.Fields[f.Focus]
			if field.Kind == TextField || field.Kind == PasswordField {
				field.Value += " "
				field.Error = ""
			} else if field.Kind == MultiChoiceField && field.Value != "" {
				field.Selected = toggle(field.Selected, field.Value)
			} else if field.Kind == YesNoField {
				if field.Value == "Yes" {
					field.Value = "No"
				} else {
					field.Value = "Yes"
				}
			}
		}
		return f, nil
	}
	if k.Text != "" && f.Focus < len(f.Fields) {
		field := &f.Fields[f.Focus]
		switch field.Kind {
		case TextField, PasswordField:
			field.Value += k.Text
			field.Error = ""
		case YesNoField:
			if strings.EqualFold(k.Text, "y") {
				field.Value = "Yes"
			} else if strings.EqualFold(k.Text, "n") {
				field.Value = "No"
			}
		case MultiChoiceField:
			for _, opt := range field.Options {
				if len([]rune(opt)) > 0 && unicode.ToLower([]rune(opt)[0]) == unicode.ToLower([]rune(k.Text)[0]) {
					field.Value = opt
					break
				}
			}
		}
	}
	return f, nil
}
func optionIndex(options []string, value string) int {
	for i, v := range options {
		if v == value {
			return i
		}
	}
	return 0
}
func (f *Form) submit() tea.Cmd {
	values := map[string]string{}
	secrets := map[string]string{}
	for _, field := range f.Fields {
		if field.Kind == PasswordField && field.Value != "" {
			secrets[field.Name] = field.Value
		}
	}
	for i := range f.Fields {
		field := &f.Fields[i]
		value := field.Value
		if field.Kind == MultiChoiceField {
			value = strings.Join(field.Selected, ",")
		}
		if field.Kind == YesNoField && value == "" {
			value = "No"
		}
		field.Error = ""
		if field.Validate != nil {
			if err := field.Validate(value); err != nil {
				field.Error = redact(err.Error(), secrets)
				if field.Kind == PasswordField && field.Value != "" {
					field.Error = strings.ReplaceAll(field.Error, field.Value, "[redacted]")
				}
				f.Focus = i
				f.err = nil
				return nil
			}
		}
		values[field.Name] = value
	}
	if f.Submit != nil {
		if err := f.Submit(values); err != nil {
			f.err = errors.New(redact(err.Error(), secrets))
			return nil
		}
	}
	f.err = nil
	return func() tea.Msg { return FormResultMsg{Values: values} }
}
func (f *Form) View(width, height int) string {
	f.width, f.height = max(0, width), max(0, height)
	return f.layout(f.width, f.height)
}

func (f *Form) layout(width, height int) string {
	f.width, f.height = width, height
	lines, fieldStarts, fieldEnds := f.formLines()
	contentHeight := max(0, f.height-1-boolInt(f.err != nil))
	f.adjustScroll(lines, fieldStarts, fieldEnds, contentHeight)
	visible := make([]string, 0, contentHeight+2)
	if contentHeight >= 2 && len(f.Fields) > 0 {
		focus := min(max(0, f.Focus), len(f.Fields)-1)
		blockStart, blockEnd := fieldStarts[focus], fieldEnds[focus]
		if blockEnd-blockStart > contentHeight && f.Fields[focus].Error != "" {
			visible = append(visible, lines[blockStart], lines[blockEnd-1])
		}
	}
	if len(visible) == 0 {
		start := min(f.scroll, len(lines))
		end := min(len(lines), start+contentHeight)
		visible = append(visible, lines[start:end]...)
	}
	if f.err != nil {
		visible = append(visible, "Error: "+f.err.Error())
	}
	visible = append(visible, f.KeyHints())
	return boundLines(visible, f.width, f.height)
}

func (f *Form) formLines() ([]string, []int, []int) {
	lines := []string{f.TitleText}
	fieldStarts, fieldEnds := make([]int, len(f.Fields)), make([]int, len(f.Fields))
	for i, field := range f.Fields {
		fieldStarts[i] = len(lines)
		value := field.Value
		if field.Kind == PasswordField {
			value = strings.Repeat("*", lipgloss.Width(value))
		}
		line := fmt.Sprintf("  %s: %s", field.Name, value)
		if i == f.Focus {
			line = "> " + strings.TrimPrefix(line, "  ")
		}
		lines = append(lines, line)
		if field.Kind == SingleChoiceField || field.Kind == MultiChoiceField {
			lines = append(lines, "    ["+strings.Join(field.Options, " | ")+"]")
			if field.Kind == MultiChoiceField && len(field.Selected) > 0 {
				lines = append(lines, "    Selected: "+strings.Join(field.Selected, ", "))
			}
		}
		if field.Error != "" {
			lines = append(lines, "    Error: "+field.Error)
		}
		fieldEnds[i] = len(lines)
	}
	return lines, fieldStarts, fieldEnds
}

func (f *Form) adjustScroll(lines []string, starts, ends []int, contentHeight int) {
	f.scroll = min(max(0, f.scroll), max(0, len(lines)-1))
	if len(f.Fields) == 0 || contentHeight == 0 {
		return
	}
	focus := min(max(0, f.Focus), len(f.Fields)-1)
	start, end := starts[focus], ends[focus]
	if end-start <= contentHeight {
		if start < f.scroll {
			f.scroll = start
		}
		if end > f.scroll+contentHeight {
			f.scroll = end - contentHeight
		}
		return
	}
	f.scroll = start
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// AsModal adapts this form into the frame's Modal interface.
func (f *Form) AsModal() Modal { return formModal{form: f} }

type formModal struct{ form *Form }

func (m formModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	s, c := m.form.Update(msg)
	return formModal{form: s.(*Form)}, c
}
func (m formModal) View(w, h int) string { return m.form.View(w, h) }
func (m formModal) CapturingInput() bool { return true }
func boundLines(lines []string, width, height int) string {
	if height >= 0 && len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if width >= 0 && lipgloss.Width(line) > width {
			lines[i] = clipCells(line, width)
		}
	}
	return strings.Join(lines, "\n")
}
func toggle(values []string, value string) []string {
	for i, v := range values {
		if v == value {
			return append(values[:i], values[i+1:]...)
		}
	}
	return append(values, value)
}
func redact(s string, secrets map[string]string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
