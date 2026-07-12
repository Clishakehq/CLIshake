// Package ui implements the interactive clishake dashboard: a terminal-
// native view of agents, events, tasks, and a command/message input that
// addresses agents with @selectors.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/ask"
	"github.com/clishakehq/clishake/internal/brand"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/events"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/orchestrator"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	selStyle    = lipgloss.NewStyle().Reverse(true)
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// agentPalette holds distinct, dark-background-readable accent colors.
// Each agent gets one (stable across sessions — hashed from its name) so
// the eye can track "which agent is this" everywhere: tree, focus header,
// grid cells, activity actors.
var agentPalette = []lipgloss.Color{
	"81",  // cyan
	"213", // pink
	"114", // green
	"215", // orange
	"141", // purple
	"228", // yellow
	"75",  // blue
	"203", // coral
}

// agentStyle returns the accent style for an agent name.
func agentStyle(name string) lipgloss.Style {
	h := uint32(2166136261)
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	return lipgloss.NewStyle().Bold(true).Foreground(agentPalette[h%uint32(len(agentPalette))])
}

type tickMsg time.Time

// askPlanMsg carries the result of a background /ask translation.
type askPlanMsg struct {
	plan    ask.Plan
	backend string
	err     error
}

// askDoneMsg carries the result of executing a confirmed /ask plan.
type askDoneMsg struct {
	ran int
	err error
}

// Dashboard views. Tab cycles Overview -> Focus -> Grid -> Chat ->
// Overview; keys "1"–"4" jump directly (when the input isn't focused).
const (
	viewOverview = 1
	viewFocus    = 2
	viewGrid     = 3
	viewChat     = 4
)

// maxGridCells caps how many live agent panes the Grid view renders at once.
const maxGridCells = 6

type model struct {
	o        *orchestrator.Orchestrator
	agents   []*domain.Agent
	events   []domain.Event
	msgs     []*domain.Message
	tasks    []*domain.Task
	pending  int // pending approvals
	input    textinput.Model
	sel      int               // selected agent row
	rows     []*domain.Agent   // flattened tree in display order
	view     int               // current view: viewOverview | viewFocus | viewGrid
	preview  string            // rendered terminal screen of the selected agent (overview/focus)
	previews map[string]string // agent ID -> rendered terminal screen (grid)
	width    int
	height   int
	feedback string
	quitting bool

	// /ask state: a translation running in the background, and a proposed
	// plan awaiting the lead's y/n decision.
	askBusy    bool
	askPlan    *ask.Plan
	askBackend string

	// helpVisible shows the help overlay (any key dismisses it).
	helpVisible bool

	// chatScroll is the Chat view's scroll offset in lines from the
	// bottom: 0 = pinned to the latest message.
	chatScroll int

	// dialogAgents holds the IDs of agents whose rendered screen currently
	// shows a selection dialog (permission prompt, folder trust, ...) —
	// answerable from the dashboard with a/A/d.
	dialogAgents map[string]bool

	// sug holds selector suggestions while the input is a partial "@..."
	// token; sugSel is the highlighted entry (↑/↓ cycle, Tab accepts).
	sug    []string
	sugSel int

	// chatFilter narrows the Chat view: "" (everything), "@all"
	// (broadcasts only), an agent name or "lead" (sender or recipient),
	// or a literal selector like "@team:reviewers".
	chatFilter string

	// pendingRemove holds the agent name armed for removal: pressing x
	// once arms it, pressing x again removes agent + window. Any other
	// key disarms.
	pendingRemove string
}

// matchChatFilter reports whether a message passes the chat filter.
func matchChatFilter(msg *domain.Message, filter string) bool {
	switch {
	case filter == "":
		return true
	case filter == "@all" || filter == "all":
		return msg.Selector == "@all"
	case strings.HasPrefix(filter, "@"):
		return msg.Selector == filter
	default:
		return msg.Sender == filter || msg.Recipient == filter
	}
}

// chatFilterOptions returns the Chat view's filter chips in display order:
// everything, broadcasts, the lead, then one chip per agent.
func chatFilterOptions(agents []*domain.Agent) []string {
	opts := []string{"", "@all", "lead"}
	for _, a := range agents {
		if a.Adapter != "observed" {
			opts = append(opts, a.Name)
		}
	}
	return opts
}

// chatFilterStep returns the chip before/after current, wrapping.
func chatFilterStep(current string, agents []*domain.Agent, forward bool) string {
	opts := chatFilterOptions(agents)
	idx := 0
	for i, f := range opts {
		if f == current {
			idx = i
			break
		}
	}
	if forward {
		idx = (idx + 1) % len(opts)
	} else {
		idx = (idx - 1 + len(opts)) % len(opts)
	}
	return opts[idx]
}

// chatFilterLabel renders a chip's display name.
func chatFilterLabel(filter string) string {
	switch filter {
	case "":
		return "everything"
	case "@all":
		return "broadcasts"
	default:
		return filter
	}
}

// buildSuggestions returns the selector completions for a partial input
// value. Active only while the value is a lone "@..." token (no space yet):
// agent names, @team:/@role: groups, open @task: ids, and @all — filtered
// by prefix against both the full selector and the bare value after any
// "kind:" prefix (so "@rev" finds "@team:reviewers" and "@role:reviewer").
func buildSuggestions(agents []*domain.Agent, tasks []*domain.Task, value string) []string {
	if !strings.HasPrefix(value, "@") || strings.ContainsAny(value, " \t") {
		return nil
	}
	typed := strings.ToLower(strings.TrimPrefix(value, "@"))

	var candidates []string
	seen := map[string]bool{}
	add := func(sel string) {
		if !seen[sel] {
			seen[sel] = true
			candidates = append(candidates, sel)
		}
	}
	add("@all")
	teams := map[string]bool{}
	roles := map[string]bool{}
	for _, a := range agents {
		if !a.Status.IsTerminal() {
			add("@" + a.Name)
		}
		if a.Team != "" && !teams[a.Team] {
			teams[a.Team] = true
			add("@team:" + a.Team)
		}
		if a.Role != "" && !roles[a.Role] {
			roles[a.Role] = true
			add("@role:" + a.Role)
		}
	}
	for _, t := range tasks {
		if t.Status != domain.TaskCompleted && t.Status != domain.TaskCancelled {
			add("@task:" + t.ID)
		}
	}

	var out []string
	for _, c := range candidates {
		body := strings.ToLower(strings.TrimPrefix(c, "@"))
		bare := body
		if i := strings.IndexByte(body, ':'); i >= 0 {
			bare = body[i+1:]
		}
		if typed == "" || strings.HasPrefix(body, typed) || strings.HasPrefix(bare, typed) {
			out = append(out, c)
		}
	}
	return out
}

// hasDialogCursor reports whether a rendered screen shows a selection
// dialog: a prompt glyph followed by a numbered entry ("❯ 1. Yes").
func hasDialogCursor(screen string) bool {
	for _, g := range []string{"❯", "›", ">", "┃"} {
		rest := screen
		for {
			i := strings.Index(rest, g)
			if i < 0 {
				break
			}
			rest = rest[i+len(g):]
			t := strings.TrimLeft(rest, " ")
			d := 0
			for d < len(t) && t[d] >= '0' && t[d] <= '9' {
				d++
			}
			if d > 0 && d < len(t) && t[d] == '.' {
				return true
			}
		}
	}
	return false
}

// Mode badges: the dashboard is modal (navigate agents vs type a message),
// and the active mode must be unmistakable at a glance.
var (
	navBadge = lipgloss.NewStyle().Bold(true).Reverse(true).
			Foreground(lipgloss.Color("245")).Render(" NAV ")
	inputBadge = lipgloss.NewStyle().Bold(true).Reverse(true).
			Foreground(lipgloss.Color("205")).Render(" INPUT ")
	placeholderNav   = "press i, @ or / to type…"
	placeholderInput = "@agent message or /command…   (esc returns to navigation)"
)

// Run starts the dashboard and blocks until the user quits.
func Run(o *orchestrator.Orchestrator) error {
	ti := textinput.New()
	ti.Placeholder = placeholderNav
	ti.Prompt = "» "
	ti.PromptStyle = dimStyle
	ti.CharLimit = 4000
	m := model{o: o, input: ti, view: viewOverview}
	m.refresh()
	// Agents keep working while no dashboard is open; say so on reattach
	// instead of letting mid-flight conversations look like fresh activity.
	if !o.PrevSeen.IsZero() {
		away := 0
		for _, msg := range m.msgs {
			if msg.CreatedAt.After(o.PrevSeen) {
				away++
			}
		}
		if away > 0 {
			m.feedback = statusStyle.Render(fmt.Sprintf(
				"while you were away: %d message(s) exchanged — press 4 for the chat view", away))
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// setInputFocus switches between navigation and input mode, keeping every
// visual cue (badge, prompt color, placeholder) in sync.
func (m *model) setInputFocus(focused bool) tea.Cmd {
	if focused {
		m.input.Placeholder = placeholderInput
		m.input.PromptStyle = titleStyle
		return m.input.Focus()
	}
	m.input.Blur()
	m.input.Placeholder = placeholderNav
	m.input.PromptStyle = dimStyle
	m.sug, m.sugSel = nil, 0
	return nil
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tick(), textinput.Blink)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refresh polls the orchestrator and reloads view data.
func (m *model) refresh() {
	m.o.Poll()
	if agents, err := m.o.Store.ListAgents(); err == nil {
		m.agents = agents
	}
	if evs, err := events.Tail(m.o.Log.Path(), 200); err == nil {
		m.events = evs
	}
	if msgs, err := m.o.Store.ListMessages(500); err == nil {
		m.msgs = msgs
	}
	if ts, err := m.o.Tasks.List(); err == nil {
		m.tasks = ts
	}
	if aps, err := m.o.Store.ListApprovals(domain.ApprovalPending); err == nil {
		m.pending = len(aps)
	}
	m.rows = flatten(m.agents)
	if m.sel >= len(m.rows) {
		m.sel = max(0, len(m.rows)-1)
	}
	m.preview = ""
	m.previews = nil
	m.dialogAgents = map[string]bool{}
	// Capture panes only for what the current view needs: overview/focus
	// only ever show the selected agent; grid shows every live agent
	// (capped). Exited agents keep their pane (remain-on-exit), so their
	// FINAL screen stays visible instead of a blank panel.
	switch m.view {
	case viewGrid:
		m.previews = m.captureGrid()
		for id, screen := range m.previews {
			if hasDialogCursor(screen) {
				m.dialogAgents[id] = true
			}
		}
	default:
		if len(m.rows) > 0 {
			if a := m.rows[m.sel]; a.Tmux.PaneID != "" {
				if captured, err := m.o.Tmux.CapturePane(a.Tmux.PaneID, 0); err == nil {
					m.preview = ansi.StripLiteralEscapes(strings.TrimRight(captured, " \n"))
					if hasDialogCursor(m.preview) {
						m.dialogAgents[a.ID] = true
					}
				}
			}
		}
	}
}

// captureGrid captures the live terminal screens of every grid-eligible
// agent (live status, has a pane), capped at maxGridCells, keyed by agent ID.
func (m *model) captureGrid() map[string]string {
	agents := gridAgents(m.rows, maxGridCells)
	if len(agents) == 0 {
		return nil
	}
	out := make(map[string]string, len(agents))
	for _, a := range agents {
		if captured, err := m.o.Tmux.CapturePane(a.Tmux.PaneID, 0); err == nil {
			out[a.ID] = ansi.StripLiteralEscapes(strings.TrimRight(captured, " \n"))
		}
	}
	return out
}

// flatten orders agents as a tree (parents before children).
func flatten(agents []*domain.Agent) []*domain.Agent {
	byID := map[string]*domain.Agent{}
	children := map[string][]*domain.Agent{}
	var roots []*domain.Agent
	for _, a := range agents {
		byID[a.ID] = a
	}
	for _, a := range agents {
		if a.ParentID != "" && byID[a.ParentID] != nil {
			children[a.ParentID] = append(children[a.ParentID], a)
		} else {
			roots = append(roots, a)
		}
	}
	sortAgents := func(s []*domain.Agent) {
		sort.Slice(s, func(i, j int) bool { return s[i].CreatedAt.Before(s[j].CreatedAt) })
	}
	sortAgents(roots)
	var out []*domain.Agent
	var walk func(a *domain.Agent)
	walk = func(a *domain.Agent) {
		out = append(out, a)
		kids := children[a.ID]
		sortAgents(kids)
		for _, c := range kids {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// gridIndices returns, in row order, the indices into rows of agents
// eligible for the Grid view (live status with an assigned tmux pane),
// capped at max entries.
func gridIndices(rows []*domain.Agent, max int) []int {
	if max <= 0 {
		return nil
	}
	var idx []int
	for i, a := range rows {
		if a.Status.IsLive() && a.Tmux.PaneID != "" {
			idx = append(idx, i)
			if len(idx) >= max {
				break
			}
		}
	}
	return idx
}

// gridAgents resolves gridIndices into the actual agent pointers.
func gridAgents(rows []*domain.Agent, max int) []*domain.Agent {
	idx := gridIndices(rows, max)
	if len(idx) == 0 {
		return nil
	}
	out := make([]*domain.Agent, 0, len(idx))
	for _, i := range idx {
		out = append(out, rows[i])
	}
	return out
}

// countLive counts all agents eligible for the grid (uncapped), used to
// report how many were left out when the grid is full.
func countLive(rows []*domain.Agent) int {
	n := 0
	for _, a := range rows {
		if a.Status.IsLive() && a.Tmux.PaneID != "" {
			n++
		}
	}
	return n
}

// gridDims returns the column/row shape of the Grid view for n cells:
// 1 -> 1x1 (full width), 2 -> 2x1, 3-4 -> 2x2, 5-6 (or more) -> 2x3.
func gridDims(n int) (cols, rows int) {
	switch {
	case n <= 0:
		return 0, 0
	case n == 1:
		return 1, 1
	case n == 2:
		return 2, 1
	case n <= 4:
		return 2, 2
	default:
		return 2, 3
	}
}

// stepGridSelection moves the current row index cur to the next (forward)
// or previous eligible grid index, clamping at the ends instead of
// wrapping. If cur is not itself in indices, it moves to the nearest
// neighbor in the requested direction.
func stepGridSelection(indices []int, cur int, forward bool) int {
	if len(indices) == 0 {
		return cur
	}
	if forward {
		for _, v := range indices {
			if v > cur {
				return v
			}
		}
		return indices[len(indices)-1]
	}
	for i := len(indices) - 1; i >= 0; i-- {
		if indices[i] < cur {
			return indices[i]
		}
	}
	return indices[0]
}

// nextView cycles Overview -> Focus -> Grid -> Overview.
func nextView(v int) int {
	switch v {
	case viewOverview:
		return viewFocus
	case viewFocus:
		return viewGrid
	case viewGrid:
		return viewChat
	default:
		return viewOverview
	}
}

// viewName renders the current view for the header line.
func viewName(v int) string {
	switch v {
	case viewFocus:
		return "focus"
	case viewGrid:
		return "grid"
	case viewChat:
		return "chat"
	default:
		return "overview"
	}
}

// trimTrailingBlankLines drops trailing empty/whitespace-only lines.
func trimTrailingBlankLines(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

// tailLines splits s into lines, trims trailing blank lines (tmux panes
// pad with blank rows below the cursor), and returns at most the last n
// lines.
func tailLines(s string, n int) []string {
	if n <= 0 || s == "" {
		return nil
	}
	lines := trimTrailingBlankLines(strings.Split(s, "\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refresh()
		return m, tick()
	case askPlanMsg:
		m.askBusy = false
		if msg.err != nil {
			m.feedback = errStyle.Render("ask: " + msg.err.Error())
			return m, nil
		}
		p := msg.plan
		m.askPlan = &p
		m.askBackend = msg.backend
		m.feedback = warnStyle.Render("plan ready — y to run, n to cancel")
		return m, nil
	case askDoneMsg:
		m.askBusy = false
		if msg.err != nil {
			m.feedback = errStyle.Render(fmt.Sprintf("ask: %d step(s) ok, then: %v", msg.ran, msg.err))
		} else {
			m.feedback = okStyle.Render(fmt.Sprintf("ask: ran %d command(s)", msg.ran))
		}
		m.refresh()
		return m, nil
	case tea.KeyMsg:
		// The help overlay is modal: any key dismisses it.
		if m.helpVisible {
			m.helpVisible = false
			return m, nil
		}
		// A pending /ask plan is modal: only y (run) and n/esc (cancel).
		if m.askPlan != nil && !m.input.Focused() {
			switch msg.String() {
			case "y", "Y":
				plan := *m.askPlan
				m.askPlan = nil
				m.askBusy = true
				m.feedback = "running plan…"
				return m, runAskPlan(plan)
			case "n", "N", "esc", "q", "ctrl+c":
				m.askPlan = nil
				m.feedback = "ask plan cancelled — nothing was run"
				return m, nil
			}
			return m, nil
		}
		// Any key other than a second x disarms a pending removal.
		if m.pendingRemove != "" && msg.String() != "x" {
			m.pendingRemove = ""
		}
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "x":
			if !m.input.Focused() && len(m.rows) > 0 {
				a := m.rows[m.sel]
				if m.pendingRemove != a.Name {
					m.pendingRemove = a.Name
					m.feedback = warnStyle.Render(fmt.Sprintf(
						"remove %s and close its window? press x again to confirm", a.Name))
					return m, nil
				}
				m.pendingRemove = ""
				if err := m.o.RemoveAgent(a.Name); err != nil {
					m.feedback = errStyle.Render(err.Error())
				} else {
					m.feedback = okStyle.Render("removed " + a.Name + " (window closed)")
				}
				m.refresh()
				return m, nil
			}
		case "q":
			if m.input.Value() == "" && !m.input.Focused() {
				m.quitting = true
				return m, tea.Quit
			}
		case "esc":
			if m.input.Focused() {
				m.setInputFocus(false)
				return m, nil
			}
		case "i", "/", "@":
			if !m.input.Focused() {
				m.setInputFocus(true)
				if msg.String() != "i" {
					m.input.SetValue(msg.String())
					m.input.CursorEnd()
				}
				m.sug = buildSuggestions(m.agents, m.tasks, m.input.Value())
				m.sugSel = 0
				return m, textinput.Blink
			}
		case "up", "k":
			if !m.input.Focused() {
				switch {
				case m.view == viewChat:
					m.chatScroll++
					m.clampChatScroll()
				case m.view == viewGrid:
					if idx := gridIndices(m.rows, maxGridCells); len(idx) > 0 {
						m.sel = stepGridSelection(idx, m.sel, false)
					}
				case m.sel > 0:
					m.sel--
				}
				return m, nil
			}
		case "down", "j":
			if !m.input.Focused() {
				switch {
				case m.view == viewChat:
					if m.chatScroll > 0 {
						m.chatScroll--
					}
				case m.view == viewGrid:
					if idx := gridIndices(m.rows, maxGridCells); len(idx) > 0 {
						m.sel = stepGridSelection(idx, m.sel, true)
					}
				case m.sel < len(m.rows)-1:
					m.sel++
				}
				return m, nil
			}
		case "left", "h", "right", "l":
			if !m.input.Focused() && m.view == viewChat {
				forward := msg.String() == "right" || msg.String() == "l"
				m.chatFilter = chatFilterStep(m.chatFilter, m.agents, forward)
				m.chatScroll = 0
				return m, nil
			}
		case "pgup":
			if !m.input.Focused() && m.view == viewChat {
				m.chatScroll += max(1, m.height-10)
				m.clampChatScroll()
				return m, nil
			}
		case "pgdown":
			if !m.input.Focused() && m.view == viewChat {
				m.chatScroll -= max(1, m.height-10)
				if m.chatScroll < 0 {
					m.chatScroll = 0
				}
				return m, nil
			}
		case "g", "home":
			if !m.input.Focused() && m.view == viewChat {
				m.chatScroll = 1 << 30
				m.clampChatScroll() // resolves to the oldest message immediately
				return m, nil
			}
		case "G", "end":
			if !m.input.Focused() && m.view == viewChat {
				m.chatScroll = 0
				return m, nil
			}
		case "tab":
			if !m.input.Focused() {
				m.view = nextView(m.view)
				m.chatScroll = 0
				m.refresh()
				return m, nil
			}
		case "1", "2", "3", "4":
			if !m.input.Focused() {
				m.view = int(msg.String()[0] - '0')
				m.chatScroll = 0
				m.refresh()
				return m, nil
			}
		case "enter":
			if m.input.Focused() {
				line := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				if line == "" {
					return m, nil
				}
				return m.execLine(line)
			}
			// enter on a selected agent = focus its pane
			if len(m.rows) > 0 {
				return m.focusSelected()
			}
		case "f":
			if !m.input.Focused() && len(m.rows) > 0 {
				return m.focusSelected()
			}
		case "a", "A", "d":
			// Answer a harness dialog (permission prompt, folder trust)
			// on the selected agent without attaching: a = accept the
			// highlighted option, A = second option (usually "always
			// allow/remember"), d = dismiss (Escape).
			if !m.input.Focused() && len(m.rows) > 0 {
				a := m.rows[m.sel]
				if !m.dialogAgents[a.ID] {
					m.feedback = warnStyle.Render("no dialog detected on " + a.Name)
					return m, nil
				}
				var err error
				switch msg.String() {
				case "a":
					err = m.o.Tmux.SendKeys(a.Tmux.PaneID, "Enter")
					m.feedback = okStyle.Render("accepted dialog on " + a.Name)
				case "A":
					if err = m.o.Tmux.SendKeys(a.Tmux.PaneID, "Down"); err == nil {
						time.Sleep(200 * time.Millisecond)
						err = m.o.Tmux.SendKeys(a.Tmux.PaneID, "Enter")
					}
					m.feedback = okStyle.Render("chose second option on " + a.Name)
				case "d":
					err = m.o.Tmux.SendKeys(a.Tmux.PaneID, "Escape")
					m.feedback = "dismissed dialog on " + a.Name
				}
				if err != nil {
					m.feedback = errStyle.Render(err.Error())
				}
				m.refresh()
				return m, nil
			}
		case "s":
			if !m.input.Focused() && len(m.rows) > 0 {
				a := m.rows[m.sel]
				if err := m.o.StopAgent(a.Name, true); err != nil {
					m.feedback = errStyle.Render(err.Error())
				} else {
					m.feedback = "stopped " + a.Name
				}
				m.refresh()
				return m, nil
			}
		case "r":
			if !m.input.Focused() && len(m.rows) > 0 {
				a := m.rows[m.sel]
				if _, err := m.o.RestartAgent(a.Name); err != nil {
					m.feedback = errStyle.Render(err.Error())
				} else {
					m.feedback = "restarted " + a.Name
				}
				m.refresh()
				return m, nil
			}
		}
	}
	if m.input.Focused() {
		// Selector autocomplete: while the input is a partial "@..."
		// token, ↑/↓ cycle the suggestions and Tab accepts one.
		if key, ok := msg.(tea.KeyMsg); ok && len(m.sug) > 0 {
			switch key.String() {
			case "up":
				m.sugSel = (m.sugSel - 1 + len(m.sug)) % len(m.sug)
				return m, nil
			case "down":
				m.sugSel = (m.sugSel + 1) % len(m.sug)
				return m, nil
			case "tab":
				m.input.SetValue(m.sug[m.sugSel] + " ")
				m.input.CursorEnd()
				m.sug, m.sugSel = nil, 0
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.sug = buildSuggestions(m.agents, m.tasks, m.input.Value())
		if m.sugSel >= len(m.sug) {
			m.sugSel = 0
		}
		return m, cmd
	}
	return m, nil
}

// translateAsk runs the /ask translation off the UI thread: it gathers
// session context, calls the local AI CLI backend, and validates the plan.
func translateAsk(o *orchestrator.Orchestrator, query string) tea.Cmd {
	return func() tea.Msg {
		agents, err := o.Store.ListAgents()
		if err != nil {
			return askPlanMsg{err: err}
		}
		taskList, err := o.Tasks.List()
		if err != nil {
			return askPlanMsg{err: err}
		}
		msgs, err := o.Store.ListMessages(10)
		if err != nil {
			return askPlanMsg{err: err}
		}
		sc := ask.BuildContext(o.ProjectDir, agents, taskList, msgs, o.Registry.Names())
		plan, backend, err := ask.Translate(ask.BuildPrompt(sc, query))
		if err != nil {
			return askPlanMsg{err: err}
		}
		if err := ask.Validate(plan); err != nil {
			return askPlanMsg{err: err}
		}
		return askPlanMsg{plan: plan, backend: backend}
	}
}

// runAskPlan executes a confirmed plan step by step by re-invoking the
// clishake binary (same code paths and attribution as typed commands).
func runAskPlan(plan ask.Plan) tea.Cmd {
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			return askDoneMsg{err: err}
		}
		for i, argv := range plan.Commands {
			cmd := exec.Command(self, argv...)
			cmd.Env = os.Environ()
			if out, err := cmd.CombinedOutput(); err != nil {
				return askDoneMsg{ran: i, err: fmt.Errorf("clishake %s: %v: %s",
					strings.Join(argv, " "), err, strings.TrimSpace(string(out)))}
			}
		}
		return askDoneMsg{ran: len(plan.Commands)}
	}
}

// askOverlay renders the pending plan for y/n review in place of the body.
func (m model) askOverlay(w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Proposed plan") +
		dimStyle.Render("  (translated by "+m.askBackend+")") + "\n\n")
	if m.askPlan.Explanation != "" {
		b.WriteString(m.askPlan.Explanation + "\n\n")
	}
	for i, argv := range m.askPlan.Commands {
		quoted := make([]string, len(argv))
		for j, tok := range argv {
			if strings.ContainsAny(tok, " \t\"") {
				quoted[j] = fmt.Sprintf("%q", tok)
			} else {
				quoted[j] = tok
			}
		}
		fmt.Fprintf(&b, "  %d. clishake %s\n", i+1, strings.Join(quoted, " "))
	}
	b.WriteString("\n" + okStyle.Render("y") + " run all · " + errStyle.Render("n") + "/esc cancel\n")
	return paneStyle.Width(w - 4).Height(h).Render(b.String())
}

// chatView renders the Chat view: the full conversation — lead↔agents and
// agent↔agent — one message per block (header + wrapped body), newest at
// the bottom. j/k (or ↓/↑) scroll one line, pgup/pgdn a page, g jumps to
// the oldest message and G back to the latest.
func (m model) chatView(w, h int) string {
	shown := m.chatMessages()
	total := len(m.chatMessagesWith(""))

	title := titleStyle.Render("Chat")
	if m.chatFilter == "" {
		title += dimStyle.Render(fmt.Sprintf("  %d message(s) · @… to write", total))
	} else {
		title += dimStyle.Render(fmt.Sprintf("  %d/%d message(s) · @… to write", len(shown), total))
	}

	if total == 0 {
		return title + "\n\n" +
			dimStyle.Render("no messages yet — @<agent> <text> or @all <text>")
	}

	// Filter chips: h/l (or ←/→) switch, filtering applies instantly.
	var chips []string
	for _, f := range chatFilterOptions(m.agents) {
		label := chatFilterLabel(f)
		switch {
		case f == m.chatFilter && f != "" && f != "@all" && f != "lead":
			chips = append(chips, agentStyle(f).Reverse(true).Render(" "+label+" "))
		case f == m.chatFilter:
			chips = append(chips, selStyle.Render(" "+label+" "))
		default:
			chips = append(chips, dimStyle.Render(label))
		}
	}
	chipBar := strings.Join(chips, "  ") + dimStyle.Render("   h/l filter")

	visible, scroll, maxScroll := windowLines(m.chatLines(w), h-4, m.chatScroll)
	switch {
	case maxScroll == 0:
		// everything fits; no indicator needed
	case scroll == 0:
		title += dimStyle.Render("  · ↑ k to scroll back")
	default:
		title += warnStyle.Render(fmt.Sprintf("  · scrolled ↑%d/%d — j down · G latest", scroll, maxScroll))
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(truncLine(chipBar, w) + "\n\n")
	if len(shown) == 0 {
		b.WriteString(dimStyle.Render("no messages match this filter — l/h to move on"))
		return b.String()
	}
	for _, l := range visible {
		b.WriteString(truncLine(l, w) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// chatMessages returns the messages the Chat view shows: failed deliveries
// are hidden (they never reached anyone — `clishake messages` still lists
// them for debugging), and multi-recipient fan-outs (broadcasts, role/team
// sends) collapse to a single entry via the router's shared group ID.
func (m model) chatMessages() []*domain.Message {
	return m.chatMessagesWith(m.chatFilter)
}

func (m model) chatMessagesWith(filter string) []*domain.Message {
	var out []*domain.Message
	seenGroup := map[string]bool{}
	for _, msg := range m.msgs {
		if msg.Delivery == domain.DeliveryFailed {
			continue
		}
		if g := msg.Meta["group"]; g != "" {
			if seenGroup[g] {
				continue
			}
			seenGroup[g] = true
		}
		if !matchChatFilter(msg, filter) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// chatRecipientLabel renders where a message went: the selector for
// fan-outs ("all", "role:reviewer"), the concrete recipient otherwise.
func chatRecipientLabel(msg *domain.Message, agentNames map[string]bool) string {
	sel := strings.TrimPrefix(msg.Selector, "@")
	if sel != "" && sel != msg.Recipient {
		return titleStyle.Render(sel)
	}
	return actorLabel(msg.Recipient, agentNames)
}

// chatLines flattens every displayed message into lines (chronological):
// a header line, the wrapped body, and a spacer per message.
func (m model) chatLines(w int) []string {
	agentNames := map[string]bool{}
	for _, a := range m.rows {
		agentNames[a.Name] = true
	}
	bodyW := w - 6
	if bodyW < 20 {
		bodyW = 20
	}
	var all []string
	for _, msg := range m.chatMessages() {
		all = append(all, fmt.Sprintf("%s %s → %s%s",
			dimStyle.Render(msg.CreatedAt.Local().Format("15:04:05")),
			actorLabel(msg.Sender, agentNames), chatRecipientLabel(msg, agentNames),
			chatMeta(msg)))
		for _, l := range wrapText(msg.Body, bodyW) {
			all = append(all, "   "+l)
		}
		all = append(all, "")
	}
	return all
}

// clampChatScroll bounds the scroll offset to the existing content so
// jump-to-top and stale offsets resolve immediately (not on the next tick).
func (m *model) clampChatScroll() {
	if m.chatScroll <= 0 || m.width == 0 {
		return
	}
	visH := (m.height - 7) - 4
	maxScroll := len(m.chatLines(m.width-4)) - visH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.chatScroll > maxScroll {
		m.chatScroll = maxScroll
	}
}

// windowLines returns the height-sized window of lines ending `scroll`
// lines above the bottom, with the offset clamped to what exists.
func windowLines(lines []string, height, scroll int) (visible []string, clamped, maxScroll int) {
	if height <= 0 {
		return nil, 0, 0
	}
	maxScroll = len(lines) - height
	if maxScroll < 0 {
		maxScroll = 0
	}
	clamped = scroll
	if clamped > maxScroll {
		clamped = maxScroll
	}
	if clamped < 0 {
		clamped = 0
	}
	end := len(lines) - clamped
	start := end - height
	if start < 0 {
		start = 0
	}
	return lines[start:end], clamped, maxScroll
}

// chatMeta annotates task-linked messages. (Broadcasts need no tag — the
// recipient column already reads "all" / "role:x".)
func chatMeta(msg *domain.Message) string {
	if msg.TaskID == "" {
		return ""
	}
	return dimStyle.Render(" [" + msg.TaskID + "]")
}

// wrapText wraps s to width w on word boundaries (single-line inputs; long
// words are hard-split).
func wrapText(s string, w int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		for len(word) > w {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			lines = append(lines, word[:w])
			word = word[w:]
		}
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// helpOverlay renders a readable command/key reference panel.
func (m model) helpOverlay(w, h int) string {
	row := func(k, desc string) string {
		return fmt.Sprintf("  %s  %s", titleStyle.Render(fmt.Sprintf("%-28s", k)), desc)
	}
	sections := []string{
		titleStyle.Render("Input line") + dimStyle.Render("  (press i, @ or / to focus; esc to leave · type @ for suggestions: ↑/↓ select, tab complete)"),
		row("@<name|role:x|team:x|all> <msg>", "message one agent, a group, or everyone"),
		row("@task:<task-id> <msg>", "message a task's owner + contributors"),
		row("/ask <intent>", "natural language → reviewed plan of commands"),
		row("/add <name> [adapter] [role]", "register and start an agent"),
		dimStyle.Render("      name: " + domain.AgentNameHint + ", case-insensitive"),
		row("/stop /restart /remove <name>", "agent lifecycle"),
		row("/set <agent> role|team <value>", "re-cluster a live agent ('-' clears)"),
		row("/task <title…>", "create a task"),
		row("/assign <task-id> <agent>", "assign a task (notifies the agent)"),
		row("/grant /deny <approval-id>", "decide an approval request"),
		row("/note <text…>", "append a shared note (context/notes.md)"),
		row("/clean", "close orphan tmux windows no agent owns"),
		row("/quit", "leave the dashboard (agents keep running)"),
		"",
		titleStyle.Render("Keys") + dimStyle.Render("  (when the input is not focused)"),
		row("Tab · 1 2 3 4", "switch view: overview / focus / grid / chat"),
		row("j/k or ↓/↑", "select agent · scroll chat"),
		row("pgup/pgdn · g · G", "chat: page up/down · oldest · latest"),
		row("h/l or ←/→", "chat: switch filter chip (everything/broadcasts/lead/agent)"),
		row("enter or f", "open the agent's terminal (F12 or C-b d returns)"),
		row("s · r · x x", "stop · restart · remove (press x twice) the selected agent"),
		row("a · A · d", "answer a harness dialog: accept · 2nd option · dismiss"),
		row("q or ctrl+c", "quit the dashboard"),
		"",
		dimStyle.Render("press any key to close"),
	}
	return paneStyle.Width(w - 4).Height(h).Render(strings.Join(sections, "\n"))
}

// focusSelected suspends the TUI and attaches tmux on the agent's window.
func (m model) focusSelected() (tea.Model, tea.Cmd) {
	a := m.rows[m.sel]
	if a.Tmux.Window == "" {
		m.feedback = warnStyle.Render(a.Name + " has no terminal (observed sub-agent)")
		return m, nil
	}
	if err := m.o.FocusAgent(a.Name); err != nil {
		m.feedback = errStyle.Render(err.Error())
		return m, nil
	}
	argv := m.o.AttachArgs()
	c := exec.Command(argv[0], argv[1:]...)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return tickMsg(time.Now())
	})
}

// execLine handles input lines: "@selector message" or "/command args".
func (m model) execLine(line string) (tea.Model, tea.Cmd) {
	switch {
	case strings.HasPrefix(line, "@"):
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			m.feedback = warnStyle.Render("usage: @selector message")
			return m, nil
		}
		msgs, err := m.o.Send(domain.LeadSender, parts[0], parts[1], messaging.SendOpts{})
		if err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render(fmt.Sprintf("sent to %d agent(s)", len(msgs)))
		}
	case strings.HasPrefix(line, "/"):
		return m.execCommand(line)
	default:
		m.feedback = warnStyle.Render("start with @selector to message agents, or /help")
	}
	m.refresh()
	return m, nil
}

func (m model) execCommand(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "/help":
		m.helpVisible = true
		m.setInputFocus(false)
	case "/quit":
		m.quitting = true
		return m, tea.Quit
	case "/ask":
		if len(args) < 1 {
			m.feedback = warnStyle.Render("usage: /ask <what you want in plain language>")
			break
		}
		if m.askBusy {
			m.feedback = warnStyle.Render("a translation is already running")
			break
		}
		m.askBusy = true
		m.feedback = "translating intent… (a plan will appear for y/n review)"
		return m, translateAsk(m.o, strings.Join(args, " "))
	case "/set":
		if len(args) != 3 || (args[1] != "role" && args[1] != "team") {
			m.feedback = warnStyle.Render("usage: /set <agent> role|team <value>  (value '-' clears)")
			break
		}
		val := args[2]
		if val == "-" {
			val = ""
		}
		var rolePtr, teamPtr *string
		if args[1] == "role" {
			rolePtr = &val
		} else {
			teamPtr = &val
		}
		if a, err := m.o.SetAgentMeta(args[0], rolePtr, teamPtr); err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render(fmt.Sprintf("%s: role=%s team=%s", a.Name, a.Role, a.Team))
		}
	case "/clean":
		closed, err := m.o.CleanOrphans()
		switch {
		case err != nil:
			m.feedback = errStyle.Render(err.Error())
		case len(closed) == 0:
			m.feedback = "no orphan windows to clean"
		default:
			m.feedback = okStyle.Render(fmt.Sprintf("closed %d orphan window(s): %s",
				len(closed), strings.Join(closed, ", ")))
		}
	case "/note":
		if len(args) < 1 {
			m.feedback = warnStyle.Render("usage: /note <text...>")
			break
		}
		if err := m.o.AddNote(domain.LeadSender, strings.Join(args, " ")); err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render("noted → .clishake/context/notes.md")
		}
	case "/add":
		if len(args) < 1 {
			m.feedback = warnStyle.Render("usage: /add <name> [adapter] [role] · name: " + domain.AgentNameHint)
			break
		}
		spec := orchestrator.AgentSpec{Name: args[0]}
		if len(args) > 1 {
			spec.Adapter = args[1]
		}
		if len(args) > 2 {
			spec.Role = args[2]
		}
		if _, err := m.o.AddAgent(spec); err != nil {
			m.feedback = errStyle.Render(err.Error())
			break
		}
		if _, err := m.o.StartAgent(args[0]); err != nil {
			m.feedback = errStyle.Render("added but not started: " + err.Error())
		} else {
			m.feedback = okStyle.Render("started " + args[0])
		}
	case "/stop", "/restart", "/remove":
		if len(args) != 1 {
			m.feedback = warnStyle.Render("usage: " + cmd + " <name>")
			break
		}
		var err error
		switch cmd {
		case "/stop":
			err = m.o.StopAgent(args[0], true)
		case "/restart":
			_, err = m.o.RestartAgent(args[0])
		case "/remove":
			err = m.o.RemoveAgent(args[0])
		}
		if err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render(cmd[1:] + " " + args[0] + ": ok")
		}
	case "/grant", "/deny":
		if len(args) != 1 {
			m.feedback = warnStyle.Render("usage: " + cmd + " <approval-id>")
			break
		}
		if _, err := m.o.Decide(args[0], cmd == "/grant"); err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render(cmd[1:] + " " + args[0] + ": ok")
		}
	case "/task":
		if len(args) < 1 {
			m.feedback = warnStyle.Render("usage: /task <title...>")
			break
		}
		t, err := m.o.Tasks.Create(domain.LeadSender, strings.Join(args, " "), "", "", 0, nil)
		if err != nil {
			m.feedback = errStyle.Render(err.Error())
		} else {
			m.feedback = okStyle.Render("created " + t.ID)
		}
	case "/assign":
		if len(args) != 2 {
			m.feedback = warnStyle.Render("usage: /assign <task-id> <agent>")
			break
		}
		t, err := m.o.Tasks.Assign(domain.LeadSender, args[0], args[1])
		if err != nil {
			m.feedback = errStyle.Render(err.Error())
			break
		}
		_, _ = m.o.Send(domain.LeadSender, "@"+t.Owner,
			fmt.Sprintf("You are assigned task %s: %s", t.ID, t.Title),
			messaging.SendOpts{Type: domain.MsgTask, TaskID: t.ID})
		m.feedback = okStyle.Render("assigned " + t.ID + " to " + t.Owner)
	default:
		m.feedback = warnStyle.Render("unknown command " + cmd + " (/help)")
	}
	m.refresh()
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return "clishake dashboard closed. Agents keep running in tmux (clishake attach).\n"
	}
	if m.width == 0 {
		return "loading…"
	}
	bodyH := m.height - 7
	sugActive := m.input.Focused() && len(m.sug) > 0
	if sugActive {
		bodyH-- // the suggestion bar takes one line above the input
	}

	head := titleStyle.Render(" CLIshake ▸ "+viewName(m.view)+" ") +
		statusStyle.Render(fmt.Sprintf(" %s · tmux %s · %d agent(s)",
			m.o.Session.ID, m.o.Cfg.SessionName(), len(m.rows)))
	if m.pending > 0 {
		head += "  " + warnStyle.Render(fmt.Sprintf("⚠ %d approval(s) pending — /grant <id>", m.pending))
	}

	var body string
	switch {
	case m.helpVisible:
		body = m.helpOverlay(m.width, bodyH)
	case m.askPlan != nil:
		body = m.askOverlay(m.width, bodyH)
	case m.view == viewFocus:
		fw := m.width - 4
		body = paneStyle.Width(fw).Height(bodyH).Render(m.focusContent(fw, bodyH))
	case m.view == viewGrid:
		body = m.gridView(m.width, bodyH)
	case m.view == viewChat:
		cw := m.width - 4
		body = paneStyle.Width(cw).Height(bodyH).Render(m.chatView(cw, bodyH))
	default:
		body = m.overviewBody(bodyH)
	}

	// The mode badge makes the modal state unmistakable: NAV (j/k moves the
	// agent selection) vs INPUT (keys go to the message line).
	badge := navBadge
	hint := "j/k select · enter/f open terminal (F12 returns) · s stop · r restart · x x remove · Tab/1-4 view · /help"
	if m.view == viewChat {
		hint = "j/k scroll · h/l filter · pgup/pgdn page · g/G oldest/latest · @… message · /help"
	}
	if len(m.rows) > 0 && m.dialogAgents[m.rows[m.sel].ID] {
		hint = "⚠ " + m.rows[m.sel].Name + " needs input — a accept · A second option · d dismiss · enter attach"
	}
	if m.input.Focused() {
		badge = inputBadge
		hint = "enter send · esc back to navigation"
	}
	fb := m.feedback
	if fb == "" {
		fb = dimStyle.Render(hint)
	}
	sugBar := ""
	if sugActive {
		items := make([]string, 0, len(m.sug))
		for i, s := range m.sug {
			if i == m.sugSel {
				items = append(items, selStyle.Render(s))
			} else {
				items = append(items, statusStyle.Render(s))
			}
			if i >= 9 {
				items = append(items, dimStyle.Render(fmt.Sprintf("+%d more", len(m.sug)-10)))
				break
			}
		}
		sugBar = " " + truncLine(strings.Join(items, "  ")+
			dimStyle.Render("   ↑/↓ select · tab complete"), m.width-2) + "\n"
	}
	return head + "\n" + body + "\n" + sugBar + badge + " " + m.input.View() + "\n " + fb
}

// overviewBody renders the default Overview layout: agent tree + tasks on
// the left, activity on top-right, and the selected agent's live terminal
// preview bottom-right.
func (m model) overviewBody(bodyH int) string {
	leftW := m.width * 2 / 5
	rightW := m.width - leftW - 6

	left := paneStyle.Width(leftW).Height(bodyH).Render(m.agentTree(leftW, bodyH))
	var right string
	if m.preview != "" && bodyH > 16 {
		// Split the right column: activity on top, the selected agent's
		// live terminal below.
		prevH := bodyH * 2 / 5
		actH := bodyH - prevH - 2
		top := paneStyle.Width(rightW).Height(actH).Render(m.activity(rightW, actH))
		bottom := paneStyle.Width(rightW).Height(prevH).Render(m.terminalPreview(rightW, prevH))
		right = lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	} else {
		right = paneStyle.Width(rightW).Height(bodyH).Render(m.activity(rightW, bodyH))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// focusContent renders the Focus view body: a one-line header for the
// selected agent followed by its live terminal preview filling the rest of
// the area, or a sanitized status summary if it has no live pane.
func (m model) focusContent(w, h int) string {
	if len(m.rows) == 0 {
		return dimStyle.Render("no agents — /add claude claude-code builder")
	}
	a := m.rows[m.sel]
	var b strings.Builder
	b.WriteString(truncLine(focusHeaderLine(a), w) + "\n")
	live := m.preview != ""
	if live {
		for _, l := range tailLines(m.preview, h-1) {
			b.WriteString(truncLine(l, w) + "\n")
		}
	} else {
		b.WriteString(focusStatusBlock(a, w))
	}
	return strings.TrimRight(b.String(), "\n")
}

// focusHeaderLine summarizes an agent for the Focus view header: name,
// role, adapter, status badge, branch, and current task.
func focusHeaderLine(a *domain.Agent) string {
	line := fmt.Sprintf("%s %s %s %s",
		statusDot(a.Status), agentStyle(a.Name).Render(a.Name),
		dimStyle.Render(fmt.Sprintf("(%s%s)", a.Adapter, roleSuffix(a.Role))),
		statusLabel(a.Status))
	if a.Branch != "" {
		line += dimStyle.Render("  ⎇ " + a.Branch)
	}
	if a.Task != "" {
		line += dimStyle.Render("  ↳ " + a.Task)
	}
	return line
}

// focusStatusBlock renders sanitized recent status info for an agent with
// no live terminal pane (an observed sub-agent, or a stopped/exited agent).
func focusStatusBlock(a *domain.Agent, w int) string {
	var b strings.Builder
	b.WriteString(truncLine(dimStyle.Render("status: ")+statusLabel(a.Status), w) + "\n")
	if !a.LastActivity.IsZero() {
		b.WriteString(truncLine(dimStyle.Render("last activity: "+a.LastActivity.Local().Format("2006-01-02 15:04:05")), w) + "\n")
	}
	if a.ExitCode != nil {
		b.WriteString(truncLine(dimStyle.Render(fmt.Sprintf("exit code: %d", *a.ExitCode)), w) + "\n")
	}
	hint := "no live terminal to preview"
	switch {
	case a.Tmux.Window == "":
		hint = "observed sub-agent — no managed terminal"
	case !a.Status.IsLive():
		hint = "agent not running — r to restart, enter/f to attach once live"
	}
	b.WriteString(truncLine(dimStyle.Render(hint), w) + "\n")
	return b.String()
}

// gridView renders the Grid view: every live agent with a pane shown
// simultaneously, capped at maxGridCells, laid out per gridDims.
func (m model) gridView(w, h int) string {
	idx := gridIndices(m.rows, maxGridCells)
	if len(idx) == 0 {
		fw := w - 4
		msg := "no live agent terminals — panes appear once agents start"
		if len(m.rows) == 0 {
			msg = "no agents — /add claude claude-code builder"
		}
		return paneStyle.Width(fw).Height(h).Render(dimStyle.Render(msg))
	}
	cols, gridRows := gridDims(len(idx))

	overflow := countLive(m.rows) - len(idx)
	footerH := 0
	if overflow > 0 {
		footerH = 1
	}

	cellW := w/cols - 4
	if cellW < 4 {
		cellW = 4
	}
	cellH := (h-footerH)/gridRows - 2
	if cellH < 3 {
		cellH = 3
	}

	var rowsRendered []string
	for r := 0; r < gridRows; r++ {
		var cells []string
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= len(idx) {
				cells = append(cells, paneStyle.Width(cellW).Height(cellH).Render(""))
				continue
			}
			a := m.rows[idx[i]]
			cells = append(cells, m.gridCell(a, cellW, cellH, idx[i] == m.sel))
		}
		rowsRendered = append(rowsRendered, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, rowsRendered...)
	if overflow > 0 {
		body += "\n" + dimStyle.Render(fmt.Sprintf("+%d more (use overview)", overflow))
	}
	return body
}

// gridCell renders one agent's cell in the Grid view: a header line (badge,
// name, adapter, status) then the tail of its captured screen.
func (m model) gridCell(a *domain.Agent, w, h int, selected bool) string {
	header := fmt.Sprintf("%s %s %s", statusDot(a.Status), agentStyle(a.Name).Render(a.Name),
		dimStyle.Render(fmt.Sprintf("(%s) %s", a.Adapter, a.Status)))
	if m.dialogAgents[a.ID] {
		header += warnStyle.Render(" ⚠ needs input (a/A/d)")
	}
	if selected {
		header = selStyle.Render(fmt.Sprintf("%s %s (%s) %s", statusDot(a.Status), a.Name, a.Adapter, a.Status))
	}
	var b strings.Builder
	b.WriteString(truncLine(header, w) + "\n")
	// Terminal content renders in the default foreground — dimming it made
	// agent output unreadable; dim stays reserved for chrome and metadata.
	for _, l := range tailLines(m.previews[a.ID], h-1) {
		b.WriteString(truncLine(l, w) + "\n")
	}
	cell := paneStyle
	if selected {
		cell = cell.BorderForeground(agentStyle(a.Name).GetForeground())
	}
	return cell.Width(w).Height(h).Render(strings.TrimRight(b.String(), "\n"))
}

// agentTree renders the hierarchical agent panel.
func (m model) agentTree(w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Agents") + "\n")
	if len(m.rows) == 0 {
		// Empty session: the brand moment. The banner fills the space a
		// roster will occupy once agents exist.
		if w > 42 && h > 12 {
			b.WriteString("\n" + brand.ColorBanner("") + "\n")
		}
		b.WriteString(dimStyle.Render("no agents yet — add one to get started:") + "\n")
		b.WriteString(dimStyle.Render("  /add claude claude-code builder") + "\n")
		b.WriteString(dimStyle.Render("  /add codex codex reviewer") + "\n")
		b.WriteString(dimStyle.Render("  or /ask \"spin up a builder and a reviewer\""))
		return b.String()
	}
	depth := map[string]int{}
	byID := map[string]*domain.Agent{}
	for _, a := range m.rows {
		byID[a.ID] = a
	}
	for _, a := range m.rows {
		d := 0
		p := a
		for p.ParentID != "" && byID[p.ParentID] != nil {
			d++
			p = byID[p.ParentID]
		}
		depth[a.ID] = d
	}
	for i, a := range m.rows {
		badge := statusDot(a.Status)
		line := fmt.Sprintf("%s%s %s %s",
			strings.Repeat("  ", depth[a.ID]), badge, agentStyle(a.Name).Render(a.Name),
			dimStyle.Render(fmt.Sprintf("(%s%s) %s", a.Adapter, roleSuffix(a.Role), a.Status)))
		if a.Branch != "" {
			line += dimStyle.Render(" ⎇ " + a.Branch)
		}
		if m.dialogAgents[a.ID] {
			line += warnStyle.Render(" ⚠ needs input")
		}
		if i == m.sel {
			if m.input.Focused() {
				// Input mode: keep the selection visible but subdued so
				// it's clear j/k won't move it right now.
				line = warnStyle.Render("▸") + line
			} else {
				line = selStyle.Render(fmt.Sprintf("%s%s %s (%s%s) %s",
					strings.Repeat("  ", depth[a.ID]), badge, a.Name, a.Adapter, roleSuffix(a.Role), a.Status))
			}
		}
		b.WriteString(truncLine(line, w) + "\n")
		if a.Task != "" {
			b.WriteString(truncLine(dimStyle.Render(strings.Repeat("  ", depth[a.ID])+"  ↳ "+a.Task), w) + "\n")
		}
	}
	// Task summary at the bottom of the panel.
	openTasks := 0
	for _, t := range m.tasks {
		if t.Status != domain.TaskCompleted && t.Status != domain.TaskCancelled {
			openTasks++
		}
	}
	b.WriteString("\n" + titleStyle.Render("Tasks") + dimStyle.Render(fmt.Sprintf(" %d open / %d", openTasks, len(m.tasks))) + "\n")
	shown := 0
	for i := len(m.tasks) - 1; i >= 0 && shown < 6; i-- {
		t := m.tasks[i]
		owner := t.Owner
		if owner == "" {
			owner = "unassigned"
		}
		b.WriteString(truncLine(fmt.Sprintf("%s %s %s", taskDot(t.Status), t.ID, t.Title)+dimStyle.Render(" @"+owner), w) + "\n")
		shown++
	}
	return b.String()
}

// activity renders recent events + messages interleaved (events already
// include message.sent, so show events plus the latest chat bodies).
func (m model) activity(w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Activity") + "\n")
	rows := h - 3
	evs := m.events
	if len(evs) > rows {
		evs = evs[len(evs)-rows:]
	}
	agentNames := map[string]bool{}
	for _, a := range m.rows {
		agentNames[a.Name] = true
	}
	for _, ev := range evs {
		ts := dimStyle.Render(ev.Timestamp.Local().Format("15:04:05"))
		line := fmt.Sprintf("%s %s %s→%s %s", ts, evBadge(ev.Type),
			actorLabel(ev.Actor, agentNames), actorLabel(ev.Subject, agentNames), evDetail(ev))
		b.WriteString(truncLine(line, w) + "\n")
	}
	return b.String()
}

// actorLabel colors agent names with their accent color so activity lines
// are visually attributable at a glance; lead/clishake/ids stay plain.
func actorLabel(name string, agentNames map[string]bool) string {
	if agentNames[name] {
		return agentStyle(name).Render(name)
	}
	return name
}

// terminalPreview renders the selected agent's live terminal screen (from
// tmux capture-pane, so it is already plain rendered text).
func (m model) terminalPreview(w, h int) string {
	a := m.rows[m.sel]
	var b strings.Builder
	head := titleStyle.Render("Terminal — ") + agentStyle(a.Name).Render(a.Name)
	if m.dialogAgents[a.ID] {
		// A dialog is waiting: spell out the answer keys right here.
		head += warnStyle.Render("  ⚠ needs input:") +
			dimStyle.Render(" a=1st option · A=2nd · d=cancel · enter=open for more")
	} else if !a.Status.IsLive() {
		head += dimStyle.Render("  (" + string(a.Status) + " — final screen)")
	} else {
		head += dimStyle.Render("  (enter/f to interact)")
	}
	b.WriteString(head + "\n")
	// Default foreground for content — dimmed output was unreadable.
	for _, l := range tailLines(m.preview, h-3) {
		b.WriteString(truncLine(l, w) + "\n")
	}
	return b.String()
}

func evDetail(ev domain.Event) string {
	if ev.Payload == nil {
		return ""
	}
	if body, ok := ev.Payload["body"].(string); ok {
		return dimStyle.Render(body)
	}
	if to, ok := ev.Payload["to"].(string); ok {
		return dimStyle.Render("to " + to)
	}
	if reason, ok := ev.Payload["reason"].(string); ok {
		return dimStyle.Render(reason)
	}
	return ""
}

func evBadge(t domain.EventType) string {
	s := string(t)
	switch {
	case strings.HasPrefix(s, "message."):
		return okStyle.Render(s)
	case strings.HasPrefix(s, "approval."), t == domain.EvConflictDetected:
		return warnStyle.Render(s)
	case t == domain.EvAgentExited:
		return errStyle.Render(s)
	default:
		return statusStyle.Render(s)
	}
}

func statusDot(s domain.AgentStatus) string {
	switch s {
	case domain.StatusRunning, domain.StatusReady:
		return okStyle.Render("●")
	case domain.StatusFailed:
		return errStyle.Render("✗")
	case domain.StatusCompleted:
		return okStyle.Render("✓")
	case domain.StatusAwaitingApproval, domain.StatusBlocked:
		return warnStyle.Render("⚠")
	case domain.StatusStarting, domain.StatusWaiting:
		return warnStyle.Render("◌")
	default:
		return dimStyle.Render("○")
	}
}

// statusLabel renders an agent status as a colored word (same palette as
// statusDot), for the Focus view header and status block.
func statusLabel(s domain.AgentStatus) string {
	switch s {
	case domain.StatusRunning, domain.StatusReady, domain.StatusCompleted:
		return okStyle.Render(string(s))
	case domain.StatusFailed:
		return errStyle.Render(string(s))
	case domain.StatusAwaitingApproval, domain.StatusBlocked:
		return warnStyle.Render(string(s))
	case domain.StatusStarting, domain.StatusWaiting:
		return warnStyle.Render(string(s))
	default:
		return dimStyle.Render(string(s))
	}
}

func taskDot(s domain.TaskStatus) string {
	switch s {
	case domain.TaskCompleted:
		return okStyle.Render("✓")
	case domain.TaskBlocked:
		return warnStyle.Render("⚠")
	case domain.TaskInProgress, domain.TaskInReview:
		return okStyle.Render("●")
	case domain.TaskCancelled:
		return dimStyle.Render("✕")
	default:
		return dimStyle.Render("○")
	}
}

func roleSuffix(role string) string {
	if role == "" {
		return ""
	}
	return "/" + role
}

func truncLine(s string, w int) string {
	if lipgloss.Width(s) <= w-2 {
		return s
	}
	// crude but safe: cut runes until it fits
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-3 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
