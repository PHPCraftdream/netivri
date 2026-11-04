# Interactive terminal shell

**Superseded.** This file described the first version of `netivri -i`. It
works but is hard to use; the reworked v2 shell is specified in
[`tui-ux.md`](tui-ux.md), which is authoritative where the two disagree. What
ships today is the v2 described there, in short:

- A fixed-region frame: one-line header (angel state, listen address, uptime,
  version), a tab bar (Providers, Accounts, Servers), the body, and one-line
  toast and hint slots that never wrap. From 100 columns the body is two panes
  (list left, card of the selected item right); below that a single pane that
  drills down with a breadcrumb; below 60x16 only an "enlarge the window" line.
- A list component that always ends with a selectable `+ Add` line, shows a
  named empty state with that line focused, and has an in-pane filter (`/`
  opens it, typing narrows, Esc clears, Enter keeps it and selects the first
  match). A card component with field lines (Enter opens a one-line prompt)
  and action lines (Enter runs them, hotkeys printed on them).
- Prompt dialogs for one field or one question (text, secret with `Ctrl+R`
  reveal off by default, number, choice, multi-choice with Space toggles,
  yes/no, confirm), hint above the input, error under it, Save/Cancel buttons.
  Adding is a wizard walk of prompts with Back/Next and a review step.
- Mouse support: clicking tabs, rows, field and action lines and buttons;
  the wheel scrolls the focused pane.
- Screens: Providers (add/refresh/rename/countries/remove), Accounts
  (add/clone with a fresh placeholder password/clear hops/remove) and Servers
  with full management (add wizard driven by the protocol parameter schema,
  field editing, show client link with an explicit warned reveal, rotate keys,
  remove). Secrets stay masked; the only reveal is that client link.

The v1 notes below are kept only for history and no longer describe the code.

---

Historical v1 notes: `internal/tui` provided a Bubble Tea v2 shell whose CLI
hosts implemented Providers, Accounts, and Servers screens, selecting the
initial screen from `-i` entry routing. The frame owned tabs, header status,
help, toast, modal state, and background activity; screens were lazy-created
and retained across tab changes. Server inspection was read-only. Forms used
Tab/Enter to advance, and confirmations used `y`/`n`. The CLI required
terminal stdin/stdout and excluded JSON mode, otherwise reporting
`NOT_A_TERMINAL` (exit 2) or `USAGE`; tests/builds run only in CI.
