# Interactive shell: UI and UX specification (v2)

Status: specification for the rework of `netivri -i`. The first version
([`tui.md`](tui.md)) works but is hard to use. Where the two notes disagree,
this one wins.

## What went wrong (from real use)

1. **Actions are invisible.** `a add  e edit  c clone ...` in a footer is not a
   user interface. A new user cannot tell what a screen can do or how to add a
   provider, an account or a server.
2. **Big forms do not navigate.** Up/Down change a choice instead of moving
   between fields, choices are drawn as `[any | main | own]` plus `Selected:
   any`, and there is no Save or Cancel to find.
3. **The footer jumps.** Background work (`/ account activity timer`) is
   appended to the key hints, so the line changes length and wraps.
4. **No visual structure.** Plain text, no borders, no highlight of the active
   tab, the selected row or the focused field, no column headers.
5. **Servers are read-only** and the screen only says "use the CLI".

## What works elsewhere (studied)

The user pointed at another Bubble Tea program of the same author
(`claude-code-proxy`, `ccgp --interactive`) as clearly better. What makes it
easy, and what we take from it:

- **A stack of small screens, one question per screen.** A menu of the things
  you can do, each as a line in plain words (`+ add a profile`, `move to
  another provider...`, `remove the profile`), numbered `1-9` and with a hotkey
  where useful. There is always exactly one thing in focus.
- **The entity screen is "info on top, then lines to act on".** The top shows
  every field of the entity; below, each field is a line (`field   current
  value`): Enter on it opens a one-line prompt for THAT field with a hint and
  the current value; Esc cancels. Nothing is a big form.
- **Adding is a short walk**: first a menu that explains the kinds in a
  sentence each, then one prompt per step with a hint, an inline error that
  keeps you on the step, and a result line. Destructive steps are a separate
  confirm screen that names what happens.
- **The footer is static per screen type** (`up/down move · enter select ·
  esc back`) and never carries changing text.

What it lacks, and we add: structure and colour (panels, aligned columns,
column headers, a visible focus), an overview of many things at once (tables
and a detail pane), mouse support, progress for long operations, and an
explicit Back/Next/Save on every step so it never relies on knowing a key.

## Principles

- **Everything you can do is a visible line or button**, with its hotkey
  printed on it (`Add  a`). Nothing important is only a hotkey.
- **One focus, always visible.** Exactly one element has focus. The focused
  element is drawn differently (accent colour, inverse, bold) and also without
  colour (`NO_COLOR`): the focus marker is a glyph (`›`, `>`, `[ ]`,
  underline), never colour alone.
- **Arrows work everywhere.** Up/Down move between rows, lines and steps'
  options; Left/Right move between tabs and buttons (and the cursor inside a
  text input); Tab/Shift+Tab cycle regions; Enter does the obvious thing; Esc
  goes back one level; the mouse can click and scroll anything.
- **One question at a time.** Adding and editing are walks of single
  prompts (step N of M), not tall forms, so there is nothing to navigate
  between; Back is always available and keeps the answers.
- **The layout never moves.** Every region has a fixed height. Changing text
  (spinners, toasts, counters, uptime) goes into fixed-width slots, never into
  a line that wraps or grows.
- **No dead ends.** An empty list says what it is and offers the Add line,
  focused. A failed step keeps the answer and shows the reason under it.
- **Secrets stay hidden** (subscription URLs, passwords, hop credentials,
  server keys). Revealing one needs an explicit action and a warning.
- **The words of the product** (Provider, Account, Server), not the words of
  the config file; one-sentence explanations where a choice needs them.

## Building blocks

Build on the established parts, replace our own code where theirs is better:

- `bubbles/v2`: `key` (bindings as data, used by the handler AND by the help),
  `help` (short/full help generated from the bindings), `textinput`,
  `viewport` (scrolling), `spinner`; `list` and `table` as references for
  selection and paging.
- `lipgloss/v2` for ALL layout and styling (`JoinHorizontal/Vertical`,
  `Place`, borders, padding, widths, adaptive colours).
- Bubble Tea v2 mouse messages and a program view that requests mouse mode.
- Conventions of soft-serve (tabs, list, status line), glow (list + pager),
  gh-dash (tabs, aligned tables, a sidebar with the selected item), huh
  (one field in focus: label above, hint below, error in red).

## Screen layout

Fixed regions, top to bottom; heights are constants and the body takes the
rest.

```
 netivri  ● running 127.0.0.1:7890  up 9h54m  v0.0.0                 header, 1 line
 [ Providers ]  Accounts   Servers                                   tabs, 1 line
 ┌ Providers ────────────────┐┌ main ─────────────────────────────┐
 │ NAME     NODES  STATE     ││ Subscription   ••••••••••  edit   │  list (left)
 │ ▌main       23  ok        ││ Via proxy      -           edit   │  detail card (right)
 │  own        15  ok        ││ Nodes          23 (BY DE ES ...)  │
 │  + Add a provider         ││ Updated        1h ago             │
 │                           ││                                   │
 │                           ││ Refresh now      r                │  actions
 │                           ││ Rename...                         │
 │                           ││ Remove...        d                │
 └───────────────────────────┘└───────────────────────────────────┘
  Provider "main" refreshed: 23 nodes                    ⠋ 1 task   toast + activity slot, 1 line
  ↑↓ select · Enter open · Tab pane · ←→ tab · ? help · q quit      hint line, 1 line
```

- **Two panes** from 100 columns: the list on the left, the card of the
  selected item on the right (it follows the selection). Below 100 columns the
  layout is a **single pane that drills down**: the list; Enter opens the card
  as its own screen, Esc goes back, and a breadcrumb in the header line shows
  `Providers › main`. Below 60x16 only a centred "enlarge the window" line.
- The list always ends with a `+ Add a ...` line, selectable like any row and
  focused when the list is empty. `a` jumps to it from anywhere in the list.
- The card has two kinds of lines: **field lines** (`Label   value   edit`):
  Enter on one opens the one-line prompt for that field; **action lines**
  (`Refresh now  r`, `Remove...  d`): Enter runs them. Up/Down move over all of
  them; Tab moves between the list and the card; hotkeys work from the list
  too, on the selected item.
- The header, toast line and hint line are exactly one line, truncated with an
  ellipsis, never wrapped. The toast line has two slots: the message (left)
  and the activity indicator (right, fixed width); a new toast replaces the old
  one and expires by a timer message; background task labels never go into the
  hint line. The hint line is generated from the focused element's key
  bindings (`bubbles/help`), the full list opens with `?`.
- Colours: one accent, one muted, success, warning, error; adaptive for light
  and dark; `NO_COLOR` switches to bold/reverse/glyphs. Box-drawing characters
  are fine; provide an ASCII fallback for `NETIVRI_ASCII=1` or a non-UTF-8
  locale. No emoji.

## Focus model

Order inside a screen: tabs -> list -> card. Tab/Shift+Tab cycle; first focus
is the list. Left/Right in the list or the card switch tabs; on the tab bar
they move the selection. Esc: from the card back to the list; from the list
nothing (q quits). In the single-pane layout Enter drills in and Esc backs out.

- Hotkeys (`a`, `e`, `d`, `r`, ...) work while the list or the card has focus,
  never while a text input has focus.
- A disabled action is drawn dim and skipped by focus.
- Mouse: click a tab, a list row, a field or action line (selects and, on
  second click or on the printed `edit`, activates it); the wheel scrolls the
  focused list or card.

## Prompts: edit one field, answer one question

Editing a field and each step of a wizard use the same **prompt dialog** over a
dimmed screen:

```
        ┌─ Provider main: subscription URL ───────────────────┐
        │ The address your provider gave you. It is a secret: │
        │ it is stored, never shown again.                    │
        │                                                     │
        │ URL  ••••••••••••••••••••_                          │
        │                                                     │
        │ error: not a valid URL                              │
        │                                                     │
        │               [ Save ]   [ Cancel ]                 │
        └─ Enter save · Esc cancel ───────────────────────────┘
```

- Kinds: text; secret (masked, `Ctrl+R` reveals while pressed/toggled, off by
  default and never persisted); number with min/max; **choice** of N as a
  vertical list with one-sentence explanations (`›` marks the cursor, Up/Down
  move, Enter picks) so no `[a | b | c]` strings; **multi-choice** as a list
  with `[x]`/`[ ]` (Space toggles, Enter confirms); yes/no; confirm.
- A hint (what it is, an example, the allowed values) always sits above the
  input; the current value is pre-filled; the error sits under the input and
  the answer is kept.
- Enter saves/accepts, Esc cancels (a discard question only when something
  was typed), Left/Right/Home/End/Ctrl+U edit the text, buttons `[ Save ]`
  `[ Cancel ]` are focusable with Tab and clickable.
- A prompt that runs an operation shows `Saving...` and keeps its size; an
  operation error appears in the prompt and the answer stays.
- **Confirm** prompts name what happens (`Remove provider main? Accounts
  alice and bob use it.`), `[ Cancel ]  [ Remove ]` with Cancel focused by
  default, the destructive button in the error colour, `y`/`n` also work.

### Wizards (adding things)

Adding is a walk of prompts with a header `Add a provider  -  step 2 of 3`,
`[ Back ]  [ Next ]` (Back keeps the answers, Esc cancels the whole walk with
the discard question), and a final **review** step listing the answers (secrets
masked) with `[ Back ]  [ Create ]`. After creation the new item is selected in
the list and a toast says what happened (and what to do next).

- Provider: name, subscription URL (secret), via proxy (optional, skipped by
  default) -> review -> fetch with progress and a real Cancel.
- Account: login, provider (choice with node counts), countries (text with the
  known countries as a hint, validated) or `any`, exit route/direct, allowed
  clients (choice: everyone / this computer only / custom), hops optional ->
  review. The last screen says: the first password sent through the proxy
  becomes the account's password.
- Server: protocol (choice with a sentence and the transport per protocol),
  then one prompt per parameter of that protocol (bind, port, the accounts it
  serves, protocol-specific options with their defaults pre-filled and
  explained; the layer's own port-conflict message appears on the port step)
  -> review -> create -> a result screen with `[ Show client link ]`.

## Screens

### Providers
List: Name, Nodes, State. Card fields: Subscription (masked), Via proxy, Name
(rename), Pre-hop and Post-hop (state only; Clear actions return them to the global
value); info: nodes, countries summary, updated. Actions: Refresh now,
Refresh all, Countries..., Remove... (the confirm names dependent accounts).

### Accounts
List: Login, Provider, Exit; live connections and rate when the angel runs.
Card fields: Provider, Countries, Exit, Allowed clients, Pre-hop, Post-hop
(masked, `set`/`not set`, an explicit Clear action), Password (read-only line:
`set` / `waiting for the first login`). Info: route tiers and fallbacks.
Actions: Clone..., Remove...

### Servers (full management)
List: Name, Protocol, Port, State. Card fields: Bind, Port, Accounts, and the
protocol's own parameters (secrets masked). Actions: Show client link...
(explicit reveal with a warning, plain-text block), Rotate keys..., Remove...
(confirms say which clients stop working). State comes from the running angel
(running / not applied / angel stopped; with the angel stopped edits are saved
and the state line says so).

### Service
List: Angel, Checks, Accounts (state per row). Angel card: State, Listen, Process,
Uptime, Version (read-only), actions Start, Stop..., Restart... (Stop and Restart
confirm and name the open connections; Cancel is the default). Checks card:
action Run checks and the results of `netivri doctor` with details for warnings
and failures. Accounts card: live activity per account. The state is re-read
every 3 seconds. Same operations as `up`, `down`, `status`, `doctor`
(`ops_service.go`). See [`parity.md`](parity.md).

## Empty states and first run

Every list shows, in its own words, what it is and the focused `+ Add` line.
With nothing configured at all the first tab (Providers) says: `A provider is a
subscription of proxy nodes. Add one to start.` The Accounts empty state says
an account needs a provider first and offers to switch tab. A stopped angel is
shown in the header as `stopped` (muted, not an error) with the line
`changes are saved and applied when it starts`.

## Stable rendering contract (testable)

1. For every screen, layout and mode, `View(w, h)` has exactly `h` lines and no
   line is wider than `w` (ANSI-stripped, rune-width aware).
2. Changing a toast, a spinner, uptime digits, a counter or a disabled state
   changes nothing outside its slot: render twice and compare the rest.
3. Every hotkey is reachable from a visible line/button and by focus + Enter,
   and by the mouse: a test enumerates the key bindings.
4. `NO_COLOR` and ASCII mode have the same layout positions as the coloured
   rendering.
5. Golden text tests per screen at 60x16, 80x24, 100x30, 120x40 and a
   too-small size.
6. Prompts: Enter/Esc/Back/Next/Create behave as above; a failed step keeps
   the answer; masked values never reach a view, toast or error text.

## Work plan

1. **Foundation** (`internal/tui`): theme, fixed-region frame, tabs, two-pane
   and single-pane layouts, list with the Add line, card with field and action
   lines, prompt dialog kinds, wizard host (steps, Back/Next, review), button
   row, action hints from `key`+`help`, toast/activity slots, mouse; port of the
   three existing screens (same operations, same safety) to it.
2. **Server operations** (`cmd/netivri/ops_server.go`): add, update, remove,
   link, rotate and a protocol parameter schema for the wizard.
3. **Screens**: Servers with full management; Provider rename; account Clone
   and Clear-hop; filter on long lists.
4. **Docs, help, README, CHANGELOG, e2e** describing what exists.
