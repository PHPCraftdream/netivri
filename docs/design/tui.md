# Reusable interactive TUI package

`internal/tui` provides a reusable Bubble Tea v2 shell. The implementation verifies the pinned `tea.Model.View() tea.View`, `tea.BatchMsg`, `tea.Tick`, Bubbles `spinner.Model`, and Lipgloss `Width` APIs. `NO_COLOR` requests Bubble Tea's ASCII color profile.

## Design

The frame owns navigation, shared header status, toast, modal state, and background activity. Screens are created lazily, retained across tab changes, and initialized once. Environment callbacks are bound to the screen that receives them. Background results return to their requesting screen, with unique IDs so identical labels remain distinct. Modal results close the active modal and go to its opener. Header polling runs asynchronously but serially: a completed poll schedules the next two-second tick. A successful `Header{Running:false}` shows stopped status; source errors show unavailable status. Neither stops navigation. The frame reserves banner, tab, and footer rows and constrains content to terminal dimensions.

Goals: reusable keyboard-first components, explicit async messages, deterministic direct tests, safe secret display. Non-goals: application command dispatch, network clients, config persistence, server lifecycle, terminal-independent GUI, or password storage. Screens must not edit configuration directly. Password values are short-lived form data; callers must never log result values.

## Frame and Screen API

```go
type Tab struct { Title string; New func(Env) Screen }
type Options struct {
    Tabs []Tab; Start int; Home string; Env Env
    Header HeaderSource; Now func() time.Time
    Input io.Reader; Output io.Writer
}
type Header struct { Running bool; Listen string; Uptime time.Duration; Version string }
type HeaderSource interface { Header() (Header, error) }
func NewFrame(Options) *Frame
func Start(Options) error
func IO(tabs []Tab, env Env, header HeaderSource) Options

type Screen interface {
    Title() string
    Init() tea.Cmd
    Update(tea.Msg) (Screen, tea.Cmd)
    View(width, height int) string
    KeyHints() string
    CapturingInput() bool
}
type Env struct {
    Home string
    Toast func(string) tea.Cmd
    OpenModal func(Modal) tea.Cmd
    Background func(label string, work func() tea.Msg) tea.Cmd
    Now func() time.Time
}
```

Invalid `Start` values select tab zero; empty tab lists are safe. `Now` defaults to `time.Now`, and `Home` is copied into `Env.Home`. Call `Env.Background` from a screen's `Init` or `Update`: the supplied work runs as a Bubble Tea command and delivers `BackgroundMsg{Label, Screen, Value}` back to its creator. `Toast` commands emit `ToastMsg`. Modal implementations return `ModalResultMsg{Value, Err, Canceled}` or a specific result message; the frame closes the modal and routes its result to the opening screen.

## Interactive entry and key map

`Start` starts the supplied tabs; it does not parse CLI flags or choose application screens. The embedding command owns interactive flag and route mapping, terminal checks, and JSON-output exclusion. The existing CLI entry behavior is bare `-i` to Accounts, and `sub`, `account`, `server` routes to Providers, Accounts, Servers respectively; the embedding caller sets `Options.Start` for that initial tab.

| Context | Keys | Result |
| --- | --- | --- |
| Normal screen | Tab/Right; Shift-Tab/Left | Next/previous tab |
| Normal screen | 1, 2, 3 | Select tab 1–3 |
| Normal screen | `?` | Open help; Esc or `?` closes |
| Normal screen | `q`, Ctrl-C | Quit |
| Capturing screen | Tab, Shift-Tab, arrows, `q` | Delivered to screen; Ctrl-C still quits |
| Table | Up/Down, PgUp/PgDn, Home/End, j/k | Move selection and scroll |
| Form | Tab/Shift-Tab, Enter, Esc | Move fields, submit, cancel |
| Choice field | Up/Down, Space | Change/highlight and toggle choices |
| Confirm | y, n, Enter, Esc | Yes, No, default No, cancel |

## Component API

### Table

`NewTable(columns []Column, rows []Row) *Table` defaults `EmptyText` to “No rows”. Columns have `Title`, fixed `Width` (zero infers), and `Align` (`AlignLeft`, `AlignRight`, `AlignCenter`). Rows carry display `Cells` and arbitrary `Data`. `SelectedRow() (Row, bool)` returns the current row; `SetRows` replaces rows and clamps the selection. `RenderCell func(row Row, column int, value string) string` can format or mask cells. `UpdateKey(key string, visible int)` handles Up/Down, PageUp/PageDown, Home/End, and j/k; `View(width, height)` renders the visible selection window with Unicode-aware cell sizing.

### Form

`Form` is a `Screen` configured with `TitleText`, `Fields`, `Focus`, `Submit func(map[string]string) error`, and optional `Cancel`. `Form.AsModal()` adapts it for `Env.OpenModal`. A `Field` has `Name`, `Kind`, `Value`, `Options`, `Selected`, `Validate func(string) error`, and inline `Error`. Kinds are `TextField`, `PasswordField`, `SingleChoiceField`, `MultiChoiceField`, and `YesNoField`. Errors retain values. Passwords render as asterisks; password values are redacted from validator and submit error text. Successful `FormResultMsg.Values` includes entered values, including passwords; callers must not log them.

### Confirm

`Confirm{Prompt: ...}` is a default-No `Modal`: y confirms; n or Enter resolves false; Esc cancels. Results use `ConfirmResultMsg`.

### Progress and asynchronous work

`Progress{Label, Value}` clamps `Percent()` to 0–100 and renders a bounded ASCII bar. Background work is submitted by screen commands and reported using `BackgroundMsg`; frame activity indicators use the Bubbles line spinner.

## Security and roadmap boundaries

This is presentation/interaction plumbing only: no server edits, configuration writes, account-password setting, password display, or secret persistence. Masked fields and redacted validation/submit errors reduce accidental disclosure; returned values remain the caller's responsibility. Future screen work is expected as three independent screens—Providers, Accounts, and Servers—with domain actions supplied by the embedding layer.

Bubble Tea, Bubbles, and Lipgloss are MIT-licensed dependencies; the runtime stack adds a few MB to distributed binaries. Direct tests feed messages and inspect views without sleeps, network, or real terminals. Builds and tests run only in CI (`docs/testing.md`).
