package tui

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type WizardStep struct {
	Key      string
	Prompt   *Prompt
	Optional bool
	// Body renders a promptless step: it returns the content rows shown
	// below the hint row. A step with a Body and no Prompt takes no input;
	// Back and Next treat it like any other step and the review list skips
	// it.
	Body func(width, height int) []string
}
type WizardResultMsg struct {
	Values   map[string]string
	Canceled bool
}
type Wizard struct {
	TitleText               string
	Theme                   Theme
	Steps                   []WizardStep
	Values                  map[string]string
	Index                   int
	Review, Saving          bool
	Busy                    bool
	BusyText                string
	Error                   error
	Buttons                 Buttons
	width, height           int
	discard, buttonsFocused bool
	discardFocus            int
	reviewOffset            int
	initialized             map[int]bool
	dirty                   bool
}

func NewWizard(title string, steps []WizardStep) *Wizard {
	return &Wizard{TitleText: title, Steps: steps, Values: map[string]string{}, Buttons: Buttons{Labels: []string{"Back", "Next"}}, initialized: map[int]bool{}}
}
func (w *Wizard) SetTheme(t Theme) {
	w.Theme = t
	for i := range w.Steps {
		if w.Steps[i].Prompt != nil {
			w.Steps[i].Prompt.SetTheme(t)
		}
	}
}
func (w *Wizard) CapturingInput() bool { return true }
func (w *Wizard) SetSaving(v bool)     { w.Saving = v }
func (w *Wizard) SetError(e error) {
	w.Error = e
	if w.Index >= 0 && w.Index < len(w.Steps) && w.Steps[w.Index].Prompt != nil {
		w.Steps[w.Index].Prompt.SetError(e)
	}
}
func (w *Wizard) Update(msg tea.Msg) (Modal, tea.Cmd) {
	if s, ok := msg.(tea.WindowSizeMsg); ok {
		w.width, w.height = s.Width, s.Height
		return w, nil
	}
	if w.Busy {
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
			return w, func() tea.Msg { return WizardResultMsg{Canceled: true} }
		}
		return w, nil
	}
	if w.Saving {
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
			return w, func() tea.Msg { return WizardResultMsg{Canceled: true} }
		}
		return w, nil
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok && w.Review {
		if wheel.Button == tea.MouseWheelUp {
			w.scroll(-1)
		} else if wheel.Button == tea.MouseWheelDown {
			w.scroll(1)
		}
		return w, nil
	}
	if m, ok := msg.(tea.MouseClickMsg); ok {
		if m.Button != tea.MouseLeft {
			return w, nil
		}
		if m.Y == w.height-1 && !w.Review && !w.discard {
			if i, hit := w.Buttons.Update(msg, m.Y); hit && i >= 0 {
				return w, w.activate(i)
			}
		}
		if m.Y == w.height-1 && w.Review && !w.discard {
			if i, hit := w.Buttons.Update(msg, m.Y); hit && i >= 0 {
				return w, w.activate(i)
			}
		}
		if w.Index < 0 || w.Index >= len(w.Steps) || w.Steps[w.Index].Prompt == nil {
			return w, nil
		}
		prompt := w.Steps[w.Index].Prompt
		_, c := prompt.Update(msg)
		w.Error = prompt.Error
		return w, c
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return w, nil
	}
	key := k.String()
	if w.discard {
		switch key {
		case "esc":
			w.discard = false
			return w, nil
		case "tab", "shift+tab", "left", "right":
			if key == "shift+tab" || key == "left" {
				w.discardFocus = 0
			} else {
				w.discardFocus = 1
			}
			return w, nil
		case "enter":
			if w.discardFocus == 0 {
				w.discard = false
				return w, nil
			}
			return w, func() tea.Msg { return WizardResultMsg{Canceled: true} }
		}
		return w, nil
	}
	if key == "esc" {
		if w.hasTyped() {
			w.discard = true
			w.discardFocus = 0
			return w, nil
		}
		return w, func() tea.Msg { return WizardResultMsg{Canceled: true} }
	}
	if w.Review {
		switch key {
		case "tab", "right":
			w.Buttons.Move(1)
			return w, nil
		case "shift+tab", "left":
			w.Buttons.Move(-1)
			return w, nil
		case "enter":
			return w, w.activate(w.Buttons.Focus)
		case "up":
			w.scroll(-1)
		case "down":
			w.scroll(1)
		case "pgup":
			w.scroll(-5)
		case "pgdown":
			w.scroll(5)
		case "home":
			w.reviewOffset = 0
		case "end":
			w.reviewOffset = max(0, len(w.Steps)-max(1, w.height-7))
		}
		return w, nil
	}
	if key == "tab" || key == "shift+tab" {
		if !w.buttonsFocused {
			w.buttonsFocused = true
			if key == "tab" {
				w.Buttons.Focus = 0
			} else {
				w.Buttons.Focus = 1
			}
		} else if key == "tab" && w.Buttons.Focus == 0 {
			w.Buttons.Focus = 1
		} else {
			w.buttonsFocused = false
			w.Buttons.Focus = 1
		}
		return w, nil
	}
	if w.buttonsFocused {
		if key == "left" || key == "right" {
			d := 1
			if key == "left" {
				d = -1
			}
			w.Buttons.Move(d)
			return w, nil
		}
		if key == "enter" {
			return w, w.activate(w.Buttons.Focus)
		}
		return w, nil
	}
	if key == "enter" {
		return w, w.activate(1)
	}
	if w.Index >= 0 && w.Index < len(w.Steps) && w.Steps[w.Index].Prompt != nil {
		_, c := w.Steps[w.Index].Prompt.Update(msg)
		w.Error = w.Steps[w.Index].Prompt.Error
		return w, c
	}
	return w, nil
}
func (w *Wizard) scroll(d int) {
	slots := max(1, w.height-7)
	w.reviewOffset = max(0, min(max(0, len(w.Steps)-slots), w.reviewOffset+d))
}
func (w *Wizard) hasTyped() bool {
	if w.dirty {
		return true
	}
	for _, s := range w.Steps {
		if s.Prompt != nil && s.Prompt.changed {
			return true
		}
	}
	return false
}
func (w *Wizard) activate(i int) tea.Cmd {
	if w.Review {
		if i == 0 {
			w.Review = false
			w.Index = max(0, len(w.Steps)-1)
			w.load()
			w.Buttons = Buttons{Labels: []string{"Back", "Next"}}
			w.buttonsFocused = false
			return nil
		}
		return w.result()
	}
	if w.Index < 0 || w.Index >= len(w.Steps) {
		return nil
	}
	if i == 0 {
		if w.Index > 0 {
			if !w.saveCurrent(false) {
				return nil
			}
			w.Index--
			w.load()
		}
		return nil
	}
	if !w.saveCurrent(true) {
		return nil
	}
	if w.Index < len(w.Steps)-1 {
		w.Index++
		w.load()
		w.buttonsFocused = false
		w.Buttons = Buttons{Labels: []string{"Back", "Next"}}
	} else {
		w.Review = true
		w.reviewOffset = 0
		w.Buttons = Buttons{Labels: []string{"Back", "Create"}, Focus: 1}
		w.buttonsFocused = false
	}
	return nil
}
func (w *Wizard) saveCurrent(validate bool) bool {
	if w.Index < 0 || w.Index >= len(w.Steps) {
		return false
	}
	s := w.Steps[w.Index]
	if s.Prompt == nil {
		return true
	}
	p := s.Prompt
	if validate {
		p.accept()
		if p.Error != nil {
			w.Error = p.Error
			return false
		}
	}
	if p.changed {
		w.dirty = true
	}
	v, e := p.GetAnswer()
	if e != nil {
		w.Error = e
		return false
	}
	w.Values[s.Key] = v
	w.dirty = true
	w.Error = nil
	return true
}
func (w *Wizard) load() {
	if w.Index < 0 || w.Index >= len(w.Steps) {
		return
	}
	s := w.Steps[w.Index]
	p := s.Prompt
	if p == nil {
		return
	}
	p.SetTheme(w.Theme)
	if v, ok := w.Values[s.Key]; ok {
		p.Value = v
		p.input.SetValue(v)
		if p.Kind == ChoicePrompt {
			for i, o := range p.Options {
				if o.Value == v {
					p.choice = i
					break
				}
			}
		} else if p.Kind == MultiChoicePrompt {
			p.Selected = nil
			if v != "" {
				p.Selected = strings.Split(v, ",")
			}
		}
	} else if !w.initialized[w.Index] {
		p.initialize()
		if p.Kind == MultiChoicePrompt {
			p.Value = strings.Join(p.Selected, ",")
		}
		w.initialized[w.Index] = true
	}
	p.Error = nil
	p.changed = false
	w.Error = nil
}
func (w *Wizard) result() tea.Cmd {
	v := map[string]string{}
	for k, x := range w.Values {
		v[k] = x
	}
	return func() tea.Msg { return WizardResultMsg{Values: v} }
}
func (w *Wizard) theme() Theme {
	if w.Theme.Accent == nil {
		return NewTheme()
	}
	return w.Theme
}
func (w *Wizard) View(width, height int) string {
	w.width, w.height = width, height
	rows := make([]string, height)
	put := func(y int, s string) {
		if y >= 0 && y < height {
			rows[y] = s
		}
	}
	step := min(w.Index+1, len(w.Steps))
	if len(w.Steps) == 0 {
		step = 0
	}
	put(0, fmt.Sprintf("%s - step %d of %d", w.TitleText, step, len(w.Steps)))
	put(1, "Tab: Back/Next · Esc: cancel")
	if w.discard {
		put(2, "Discard all entered values?")
		put(height-1, fmt.Sprintf("%s   %s", focusLabel("Cancel", w.discardFocus == 0), focusLabel("Discard", w.discardFocus == 1)))
	} else if w.Review {
		put(2, "Review")
		slots := max(0, height-7)
		w.reviewOffset = max(0, min(w.reviewOffset, max(0, len(w.Steps)-slots)))
		for i := 0; i < slots && w.reviewOffset+i < len(w.Steps); i++ {
			s := w.Steps[w.reviewOffset+i]
			if s.Prompt == nil { // a promptless step has no answer line
				continue
			}
			v := w.Values[s.Key]
			if s.Prompt != nil {
				v = redactWizardValue(s.Prompt, v)
			}
			label := ""
			if s.Prompt != nil {
				label = s.Prompt.Label
			}
			put(3+i, label+": "+v)
		}
		if w.Error != nil {
			put(height-3, "Error: "+w.sanitizeError(w.Error.Error()))
		}
	} else if w.Index >= 0 && w.Index < len(w.Steps) && w.Steps[w.Index].Prompt != nil {
		p := w.Steps[w.Index].Prompt
		p.SetTheme(w.theme())
		body := strings.Split(p.View(width, height), "\n")
		put(0, fmt.Sprintf("%s - step %d of %d", w.TitleText, step, len(w.Steps)))
		put(1, p.Hint)
		for i := 2; i < height-3 && i < len(body)-2; i++ {
			put(i, body[i])
		}
		if p.Error != nil {
			put(height-3, "Error: "+w.sanitizeError(p.Error.Error()))
		}
	} else if w.Index >= 0 && w.Index < len(w.Steps) && w.Steps[w.Index].Body != nil {
		for i, line := range w.Steps[w.Index].Body(width, height) {
			put(2+i, line)
		}
		if w.Error != nil {
			put(height-3, "Error: "+w.sanitizeError(w.Error.Error()))
		}
	}
	if !w.discard {
		put(height-1, w.Buttons.View(w.theme()))
	}
	if w.Saving {
		put(height-2, "Saving...")
	} else if w.Busy {
		put(height-2, w.BusyText)
	}
	return boundLines(rows, width, height)
}
func focusLabel(s string, focus bool) string {
	if focus {
		return "[" + s + "]"
	}
	return s
}
func redactWizardValue(p *Prompt, v string) string {
	if p.Kind == SecretPrompt && v != "" {
		return "********"
	}
	u, e := url.Parse(v)
	if e == nil && u.User != nil {
		u.User = url.UserPassword("[redacted]", "[redacted]")
		if u.RawQuery != "" {
			q := u.Query()
			for k := range q {
				q.Set(k, "[redacted]")
			}
			u.RawQuery = q.Encode()
		}
		return u.String()
	}
	return v
}
func (w *Wizard) sanitizeError(s string) string {
	for _, step := range w.Steps {
		if step.Prompt == nil || step.Prompt.Kind != SecretPrompt {
			continue
		}
		prompt := step.Prompt
		s = prompt.safeError(s)
		if saved, ok := w.Values[step.Key]; ok {
			s = (&Prompt{Kind: SecretPrompt, Value: saved}).safeError(s)
		}
	}
	return s
}
