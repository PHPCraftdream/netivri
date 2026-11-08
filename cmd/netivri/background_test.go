package main

import tea "charm.land/bubbletea/v2"

// inlineBackground is the Background of the screen test fixtures whose work is
// a quick read: it runs the work at once, on the caller's goroutine, and returns
// the command that delivers the message. Handing the work itself over as the
// command made parityRun run it on a goroutine and give up on it after 150 ms:
// a slow read (the race detector on a busy macOS runner) was dropped, and the
// goroutine went on writing the fixture while the test read it.
func inlineBackground(_ string, w func() tea.Msg) tea.Cmd {
	m := w()
	return func() tea.Msg { return m }
}
