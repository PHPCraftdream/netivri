# Interactive shell: UI and UX specification (v2)

Status: specification for the rework of `netivri -i`. The first version
([`tui.md`](tui.md)) works but is hard to use: actions hide in one line of
single-letter hints, forms cannot be crossed with the arrow keys, nothing is
styled or aligned, the bottom line jumps when a background label changes, and
servers cannot be created at all. This note defines what replaces it. Where the
two notes disagree, this one wins.

## What went wrong (from real use)

1. **Actions are invisible.** `a add  e edit  c clone ...` in a footer is not a
   user interface. A new user cannot tell what the screen can do.
2. **Forms do not navigate.** Up/Down do not move between fields (they change
   a choice), choices are drawn as `[any | main | own]` plus `Selected: any`,
   and there is no Save or Cancel to find.
3. **The footer jumps.** Background work (`/ account activity timer`) is
   appended to the key hints, so the line changes length and wraps.
4. **No visual structure.** Plain text, no borders, no highlight of the active
   tab, the selected row or the focused field, no column headers.
5. **Servers are read-only** and the screen only says "use the CLI".

## Principles

- **Everything you can do is on screen**, as a labelled button, and also has a
  hotkey. The hotkey is printed on the button: `[ Add  a ]`.
- **One focus, always visible.** Exactly one element has focus (tab bar, list,
  button row, a form field, a dialog button). The focused element is drawn
  differently from the rest (accent colour, inverse or bold), also without
  colour (`NO_COLOR`): the focus marker is a glyph (`>` / `[ ]` / underline),
  never colour alone.
- **Arrows work everywhere.** Up/Down move between rows and between form
  fields; Left/Right move between tabs, buttons and the options of a choice;
  Tab/Shift+Tab move between regions or fields; Enter does the obvious thing;
  Esc goes back; the mouse can click and scroll anything.
- **The layout never moves.** Every region has a fixed height. Changing text
  (spinners, timers, toasts, counters) goes into fixed-width slots, never into
  a line that wraps or grows.
- **No dead ends.** An empty list shows what it is and a focused button that
  creates the first item. A failed action keeps the user's input and says why
  next to the field.
- **Secrets stay hidden** (subscription URLs, passwords, hop credentials,
  server keys). Revealing one needs an explicit action and a warning.
- **Quiet defaults, honest names.** Labels use the words of the product
  (Provider, Account, Server), not the words of the config file.

## References (how good Bubble Tea programs are built)

Build on the established parts instead of inventing them again:

- `bubbles/v2`: `key` (key bindings as data, shared by the handler and the
  help), `help` (short and full help from those bindings), `textinput`,
  `textarea` if needed, `viewport` (scrolling), `spinner`, `list`/`table` as
  models for selection and paging. Use them where they fit; replace our own
  code by them where ours is weaker.
- `lipgloss/v2` for ALL layout and styling: `JoinHorizontal/Vertical`,
  `Place`, borders, padding, `Width/Height/MaxWidth`, adaptive colours.
- The structure of programs people like: **soft-serve** (tabs, list with a
  selection bar, status line), **glow** (list + pager, one status bar),
  **gh-dash** (tabs, aligned tables, a sidebar), **huh** forms (one field in
  focus, label above, hint below, error in red, Tab/Enter/Shift+Tab,
  Save/Cancel). Take the conventions, not the code.
- Bubble Tea v2 mouse messages (`MouseClickMsg`, `MouseWheelMsg`, ...) for
  clicks and wheel scrolling; request mouse mode in the program view.

## Screen layout

Fixed regions, top to bottom. Heights are constants; the body takes the rest.

```
 netivri   ● running  127.0.0.1:7890  up 9h54m  v0.0.0          (header, 1 line)
 ┌───────────┐┌──────────┐┌─────────┐
 │ Providers ││ Accounts ││ Servers │                            (tab bar, 1-3 lines)
 └───────────┘└──────────┘└─────────┘
 ┌──────────────────────────────────────────────────────────────┐
 │ NAME        NODES   UPDATED     STATE                          │  (column header)
 │ ▌main          23   1h ago      ok                             │  (selected row)
 │  own           15   1h ago      ok                             │
 │                                                                │
 └──────────────────────────────────────────────────────────────┘  (body, fills)
   1-2 of 2
  [ Add  a ]  [ Edit  e ]  [ Refresh  u ]  [ Remove  d ]           (button bar, 1 line)
  Provider "main" refreshed: 23 nodes                    ⠋ 1 task   (toast + activity slot, 1 line)
  ←→ tabs · ↑↓ select · Enter open · Tab focus · ? help · q quit   (hint line, 1 line)
```

Rules:

- The header, button bar, toast line and hint line are exactly one line each
  and are truncated with an ellipsis, never wrapped. Narrow terminals drop
  secondary header items in a fixed order (version, uptime, listen address).
- The toast line has two slots: the message (left, truncated) and the activity
  indicator (right, fixed width, for example `⠋ 2 tasks`). A new toast replaces
  the old one; it disappears after a few seconds by a timer message, the line
  itself keeps its height. Background task labels never go into the hint line.
- The hint line is generated from the focused element's key bindings (short
  help), never hand-written strings; the full list opens with `?`.
- Minimum size 60x16. Below it the screen shows one centred line asking for a
  bigger window and nothing else (no panic, no garbage).
- Colours: one accent colour, one muted, one success, one warning, one error;
  adaptive for light and dark backgrounds; `NO_COLOR` switches to bold,
  reverse video and glyphs. No emoji; box-drawing and the glyphs above are
  fine (they must also render in the Windows console; provide an ASCII border
  fallback for `NETIVRI_ASCII=1` or when the locale is not UTF-8).

## Focus model

The focus order inside a screen is: tab bar -> list -> button row. `Tab` and
`Shift+Tab` cycle it; the first focus is the list (or the first button when the
list is empty). Inside a region:

| Focus | Up/Down | Left/Right | Enter | Esc |
|---|---|---|---|---|
| Tab bar | to the list | previous / next tab | to the list | - |
| List | previous / next row | previous / next tab (a list has no horizontal movement) | open the row's action menu (see below) | - |
| Button row | to the list | previous / next button | press the button | to the list |

- Hotkeys (`a`, `e`, `d`, ...) work while the list or the button row has focus
  and never while a text input has focus.
- A disabled button (for example Edit with no row selected) is drawn dim and
  skipped by focus.
- `Enter` on a list row opens an **action menu**: a small bordered list at the
  row (Edit, Clone, Refresh, Details, Remove ...), Up/Down/Enter/Esc, each item
  with its hotkey. The same actions as the button row, so there is one way to
  learn both.
- Mouse: click a tab, a row (selects; double click = open the action menu), a
  button (presses it), a form field (focuses it); the wheel scrolls the list
  or the focused viewport.

## Tables

- Column headers on top, aligned columns (numbers right-aligned), widths from
  the content with a per-column min/max, the flexible column takes the rest,
  truncation with an ellipsis (rune-width aware, including CJK and ANSI-free).
- The selected row has a background bar and a `▌` marker (`>` in ASCII mode),
  also when the table is not focused (dimmer).
- A position indicator under the table (`3-12 of 47`) and a scrollbar glyph
  column when it does not fit. PgUp/PgDn/Home/End and the wheel work.
- Optional filter: `/` opens a one-line filter (type to narrow, Esc clears);
  only on lists that can be long (accounts, nodes/countries).

## Dialogs and forms

Forms are centred modal dialogs drawn over the screen (the screen stays
visible, dimmed), not full-screen replacements.

```
            ┌─ Add provider ─────────────────────────────────┐
            │                                                │
            │  Name                                          │
            │  ▌main_                                        │
            │  A short name used in accounts (letters, -, _) │
            │                                                │
            │  Subscription URL                               │
            │  ••••••••••••••••••••                           │
            │  Kept secret; never shown again                 │
            │                                                │
            │  Via proxy (optional)                           │
            │  _                                             │
            │                                                │
            │              [ Save ]   [ Cancel ]             │
            └─ ↑↓ field · Enter next · Ctrl+S save · Esc cancel ─┘
```

- One field has focus: marker `▌` (or `>`), the label bold, the input
  highlighted. Labels sit above inputs; a muted help line sits below each
  field; an error sits under the field in the error colour and moves focus to
  it on submit.
- **Up/Down and Tab/Shift+Tab move between fields; Enter moves to the next
  field; on the last field Enter focuses Save.** Left/Right edit inside a text
  input (cursor) or change a choice. Ctrl+S saves from anywhere. Esc cancels
  (asks "Discard changes?" only when something was typed).
- Field kinds:
  - text; password (masked, with a `Ctrl+R` reveal toggle that is OFF by
    default and never persists); number with min/max; multi-line only if a
    protocol needs it.
  - **choice** (one of N): drawn as a row of options `( ) any  (•) main  ( ) own`
    with Left/Right to move and select; if it has more than 4 options or long
    labels, drawn as a drop-down: Enter/Space opens an inline list.
  - **multi-choice**: `[x] main  [ ] own`, Left/Right move, Space toggles.
  - **yes/no**: `[ Yes ]  No` with Left/Right/Space.
- Buttons at the bottom are focus stops: Save (default accent), Cancel. Enter
  on Save submits; the dialog shows `Saving...` and keeps its size while the
  operation runs; an operation error appears at the top of the dialog and next
  to the field when it names one, and the input is kept.
- A dialog taller than the terminal scrolls with the focused field kept in
  view (and the Save/Cancel row pinned).
- **Confirm** dialogs: bordered message, `[ Cancel ]  [ Delete ]` with Cancel
  focused by default, Left/Right/Tab/Enter, `y`/`n` also work, destructive
  button in the error colour.

## Empty states and first run

- Every list has an empty state in the body: what this is, one sentence of
  what to do, and the Add button focused. Example: `No providers yet. A
  provider is a subscription of proxy nodes.  [ Add provider ]`.
- When the config has nothing at all, Providers is the first tab with the
  empty state; the Accounts empty state says that an account needs a provider
  first and offers to switch tab.
- The header shows the angel state; when it is stopped the status area says
  `stopped: changes are saved and applied when it starts` (muted, not an
  error).

## Screens

### Providers

Columns: Name, Nodes, Updated, State, Countries (summary). Buttons: Add,
Edit, Refresh, Refresh all, Countries, Remove. Edit changes name/URL/via
(URL field empty = unchanged). Add runs the fetch with a progress line in the
dialog (`Fetching... 12s`) and a Cancel that really cancels it. Countries
opens a scrollable dialog with the counts. Remove confirm names the accounts
that use it.

### Accounts

Columns: Login, Provider, Countries, Exit, Clients, Hops, Password, Conns,
Rate. Buttons: Add, Edit, Clone, Details, Remove. The form has: Login,
Provider (choice), Countries (text with the known countries of the provider
shown as a muted hint and a picker on `Ctrl+L`), Exit (route / direct),
Allowed clients, Pre-hop, Post-hop (masked; "set"/"not set" shown, empty =
unchanged, a Clear toggle). Details shows the route tiers.

### Servers (full management)

Columns: Name, Protocol, Bind, Port, Accounts, State, Counters. Buttons: Add,
Edit, Link, Rotate keys, Remove.

- **Add is a two-step wizard**: (1) choose the protocol from a list with a
  one-line description and the transport (TCP/UDP) of each; (2) a form built
  from the protocol's parameters (bind, port, accounts it serves, TLS material
  options, ...) with the same field kinds and inline validation as the CLI
  (the server layer already rejects overlapping bind/port; show its message
  at the port field). A port that is busy or reused shows the existing hint.
- **Edit** the same form; protocol is fixed.
- **Link** opens a dialog with the client link per account (or one for all),
  hidden behind `[ Reveal ]` with a warning, with a copy-friendly plain text
  block; the link never goes to the toast or the log.
- **Rotate keys** and **Remove** are confirm dialogs that say what breaks
  (existing client links stop working).
- State comes from the running angel per server (running / not applied /
  angel stopped); with the angel stopped, edits are saved and the state says
  so.

## Stable rendering contract (testable)

1. For every screen and mode, the number of lines of `View(w, h)` equals `h`
   and no line is wider than `w` (ANSI-stripped, rune-width aware).
2. Toggling a toast, a spinner, a counter or a disabled button never changes
   the line count nor the column of any other element (golden test: render
   twice with the volatile slot changed and compare everything outside it).
3. Every action reachable by a hotkey is reachable by focus + Enter and by the
   mouse; a test enumerates the key bindings and asserts each is on a button
   or in a menu.
4. `NO_COLOR` and ASCII mode render the same layout (same positions) with
   glyph fallbacks.
5. Sizes 60x16, 80x24, 120x40 and a 5-row-too-small terminal have golden
   tests per screen (text only).

## Work plan

1. **Foundation** (`internal/tui`): theme (styles, adaptive colours, ASCII
   fallback), fixed-region frame, focus ring, button row and buttons, action
   menu, status/toast line with the activity slot, key bindings with `key` +
   `help`, mouse routing, dialog host, Table v2, Form v2 (fields above),
   Confirm v2, empty-state helper. Golden tests for the rendering contract.
2. **Operations for servers** (`cmd/netivri/ops_server.go`): add, update,
   remove, link, rotate, and a protocol parameter schema the wizard builds its
   form from; hermetic tests; CLI output unchanged.
3. **Screens**: Providers, Accounts, Servers rebuilt on the foundation (three
   independent pieces after 1 and 2).
4. **Docs, help, README, CHANGELOG, e2e**: describe what exists.
