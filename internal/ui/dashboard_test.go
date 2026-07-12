package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/clishakehq/clishake/internal/domain"
)

func liveAgent(id string) *domain.Agent {
	return &domain.Agent{ID: id, Name: id, Status: domain.StatusRunning, Tmux: domain.TmuxRef{PaneID: "%" + id}}
}

func deadAgent(id string) *domain.Agent {
	return &domain.Agent{ID: id, Name: id, Status: domain.StatusStopped}
}

func liveNoPaneAgent(id string) *domain.Agent {
	return &domain.Agent{ID: id, Name: id, Status: domain.StatusRunning}
}

// ---------------------------------------------------------------------------
// gridDims
// ---------------------------------------------------------------------------

func TestGridDims(t *testing.T) {
	cases := []struct {
		n          int
		cols, rows int
	}{
		{0, 0, 0},
		{1, 1, 1},
		{2, 2, 1},
		{3, 2, 2},
		{4, 2, 2},
		{5, 2, 3},
		{6, 2, 3},
		{7, 2, 3}, // defensive: never called with >6 in practice, but must not panic/misbehave
	}
	for _, c := range cases {
		cols, rows := gridDims(c.n)
		if cols != c.cols || rows != c.rows {
			t.Errorf("gridDims(%d) = (%d,%d), want (%d,%d)", c.n, cols, rows, c.cols, c.rows)
		}
		if c.n >= 1 && cols*rows < c.n && c.n <= 6 {
			t.Errorf("gridDims(%d) = (%d,%d) has fewer cells (%d) than agents (%d)", c.n, cols, rows, cols*rows, c.n)
		}
	}
}

// ---------------------------------------------------------------------------
// gridIndices / gridAgents / countLive
// ---------------------------------------------------------------------------

func TestGridIndicesFiltersAndCaps(t *testing.T) {
	rows := []*domain.Agent{
		liveAgent("a"),       // 0: eligible
		deadAgent("b"),       // 1: not live
		liveNoPaneAgent("c"), // 2: live but no pane
		liveAgent("d"),       // 3: eligible
		liveAgent("e"),       // 4: eligible
		liveAgent("f"),       // 5: eligible
		liveAgent("g"),       // 6: eligible
		liveAgent("h"),       // 7: eligible
		liveAgent("i"),       // 8: eligible (should be dropped by cap, 7th eligible)
	}

	got := gridIndices(rows, maxGridCells)
	want := []int{0, 3, 4, 5, 6, 7}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gridIndices = %v, want %v", got, want)
	}

	if n := countLive(rows); n != 7 {
		t.Errorf("countLive = %d, want 7 (uncapped)", n)
	}

	agents := gridAgents(rows, maxGridCells)
	if len(agents) != 6 {
		t.Fatalf("gridAgents returned %d agents, want 6", len(agents))
	}
	for i, a := range agents {
		if a != rows[want[i]] {
			t.Errorf("gridAgents[%d] = %v, want %v", i, a, rows[want[i]])
		}
	}
}

func TestGridIndicesEmpty(t *testing.T) {
	rows := []*domain.Agent{deadAgent("a"), liveNoPaneAgent("b")}
	if idx := gridIndices(rows, maxGridCells); idx != nil {
		t.Errorf("gridIndices = %v, want nil", idx)
	}
	if agents := gridAgents(rows, maxGridCells); agents != nil {
		t.Errorf("gridAgents = %v, want nil", agents)
	}
	if idx := gridIndices(rows, 0); idx != nil {
		t.Errorf("gridIndices with max=0 = %v, want nil", idx)
	}
}

// ---------------------------------------------------------------------------
// stepGridSelection
// ---------------------------------------------------------------------------

func TestStepGridSelection(t *testing.T) {
	indices := []int{0, 2, 4}

	cases := []struct {
		cur     int
		forward bool
		want    int
	}{
		{0, true, 2},   // step forward from first
		{2, true, 4},   // step forward from middle
		{4, true, 4},   // clamp at last, no wrap
		{4, false, 2},  // step backward from last
		{2, false, 0},  // step backward from middle
		{0, false, 0},  // clamp at first, no wrap
		{1, true, 2},   // not in indices: nearest greater
		{3, true, 4},   // not in indices: nearest greater
		{1, false, 0},  // not in indices: nearest smaller
		{3, false, 2},  // not in indices: nearest smaller
		{-1, false, 0}, // below range clamps to first
		{9, true, 4},   // above range clamps to last
	}
	for _, c := range cases {
		got := stepGridSelection(indices, c.cur, c.forward)
		if got != c.want {
			t.Errorf("stepGridSelection(%v, %d, %v) = %d, want %d", indices, c.cur, c.forward, got, c.want)
		}
	}

	if got := stepGridSelection(nil, 5, true); got != 5 {
		t.Errorf("stepGridSelection(nil, 5, true) = %d, want 5 (unchanged)", got)
	}
}

// ---------------------------------------------------------------------------
// view cycling
// ---------------------------------------------------------------------------

func TestNextView(t *testing.T) {
	seq := []int{viewOverview, viewFocus, viewGrid, viewChat, viewOverview}
	v := viewOverview
	for i := 1; i < len(seq); i++ {
		v = nextView(v)
		if v != seq[i] {
			t.Fatalf("step %d: nextView -> %d, want %d", i, v, seq[i])
		}
	}
}

func TestViewName(t *testing.T) {
	cases := map[int]string{
		viewOverview: "overview",
		viewFocus:    "focus",
		viewGrid:     "grid",
		viewChat:     "chat",
		99:           "overview", // unknown falls back to overview
	}
	for v, want := range cases {
		if got := viewName(v); got != want {
			t.Errorf("viewName(%d) = %q, want %q", v, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// trimTrailingBlankLines / tailLines
// ---------------------------------------------------------------------------

func TestTrimTrailingBlankLines(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"a", "b", ""}, []string{"a", "b"}},
		{[]string{"a", "", "  ", "\t"}, []string{"a"}},
		{[]string{"", "", ""}, []string{}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{nil, []string{}},
	}
	for _, c := range cases {
		got := trimTrailingBlankLines(c.in)
		if len(got) != len(c.want) || (len(got) > 0 && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("trimTrailingBlankLines(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTailLines(t *testing.T) {
	s := "line1\nline2\nline3\n\n\n"
	got := tailLines(s, 2)
	want := []string{"line2", "line3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tailLines(%q, 2) = %v, want %v", s, got, want)
	}

	// n larger than available lines returns all (trailing blanks trimmed).
	got = tailLines(s, 10)
	want = []string{"line1", "line2", "line3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tailLines(%q, 10) = %v, want %v", s, got, want)
	}

	if got := tailLines("", 5); got != nil {
		t.Errorf("tailLines(\"\", 5) = %v, want nil", got)
	}
	if got := tailLines(s, 0); got != nil {
		t.Errorf("tailLines(s, 0) = %v, want nil", got)
	}
	if got := tailLines(s, -1); got != nil {
		t.Errorf("tailLines(s, -1) = %v, want nil", got)
	}

	// all-blank screen -> no lines.
	if got := tailLines("\n\n  \n", 5); len(got) != 0 {
		t.Errorf("tailLines(blank) = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// flatten (existing helper, sanity-covered here since it feeds gridIndices)
// ---------------------------------------------------------------------------

func TestFlattenOrdersParentsBeforeChildren(t *testing.T) {
	parent := &domain.Agent{ID: "p", Name: "parent"}
	child := &domain.Agent{ID: "c", Name: "child", ParentID: "p"}
	rows := flatten([]*domain.Agent{child, parent})
	if len(rows) != 2 || rows[0].ID != "p" || rows[1].ID != "c" {
		t.Fatalf("flatten = %v, want [p c]", rows)
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("the quick brown fox jumps", 10)
	want := []string{"the quick", "brown fox", "jumps"}
	if len(got) != len(want) {
		t.Fatalf("wrapText = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrapText[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Long words hard-split; empty input yields one empty line.
	if got := wrapText("abcdefghijk", 4); len(got) != 3 {
		t.Fatalf("hard split = %#v", got)
	}
	if got := wrapText("", 10); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty = %#v", got)
	}
}

func TestWindowLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e", "f"}

	// Pinned to bottom: last `height` lines, no scroll.
	vis, scroll, maxS := windowLines(lines, 3, 0)
	if scroll != 0 || maxS != 3 {
		t.Fatalf("scroll=%d maxScroll=%d, want 0/3", scroll, maxS)
	}
	if strings.Join(vis, "") != "def" {
		t.Fatalf("bottom window = %v", vis)
	}
	// Scrolled up two lines.
	vis, scroll, _ = windowLines(lines, 3, 2)
	if scroll != 2 || strings.Join(vis, "") != "bcd" {
		t.Fatalf("scrolled window = %v (scroll %d)", vis, scroll)
	}
	// Overscroll clamps to the top.
	vis, scroll, _ = windowLines(lines, 3, 999)
	if scroll != 3 || strings.Join(vis, "") != "abc" {
		t.Fatalf("clamped window = %v (scroll %d)", vis, scroll)
	}
	// Content shorter than the window: everything, no scroll possible.
	vis, scroll, maxS = windowLines(lines[:2], 5, 4)
	if scroll != 0 || maxS != 0 || len(vis) != 2 {
		t.Fatalf("short content: vis=%v scroll=%d max=%d", vis, scroll, maxS)
	}
	// Zero height renders nothing.
	if vis, _, _ := windowLines(lines, 0, 0); vis != nil {
		t.Fatalf("zero height = %v", vis)
	}
}

func TestChatMessagesGroupAndFilter(t *testing.T) {
	m := model{msgs: []*domain.Message{
		{ID: "m1", Sender: "lead", Recipient: "builder", Selector: "@builder",
			Delivery: domain.DeliveryDelivered, Body: "direct"},
		{ID: "m2", Sender: "lead", Recipient: "builder", Selector: "@all",
			Delivery: domain.DeliveryDelivered, Body: "bcast", Meta: map[string]string{"group": "g1"}},
		{ID: "m3", Sender: "lead", Recipient: "reviewer", Selector: "@all",
			Delivery: domain.DeliveryDelivered, Body: "bcast", Meta: map[string]string{"group": "g1"}},
		{ID: "m4", Sender: "codex", Recipient: "claude", Selector: "@claude",
			Delivery: domain.DeliveryFailed, Body: "never arrived"},
	}}
	got := m.chatMessages()
	if len(got) != 2 {
		t.Fatalf("chatMessages = %d entries, want 2 (direct + one grouped broadcast)", len(got))
	}
	if got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("wrong entries kept: %s, %s", got[0].ID, got[1].ID)
	}
	// The grouped broadcast renders with the selector, not one recipient.
	label := chatRecipientLabel(got[1], map[string]bool{"builder": true})
	if !strings.Contains(label, "all") {
		t.Fatalf("broadcast label = %q, want it to read 'all'", label)
	}
	// Direct message keeps its concrete recipient.
	if label := chatRecipientLabel(got[0], map[string]bool{"builder": true}); !strings.Contains(label, "builder") {
		t.Fatalf("direct label = %q", label)
	}
}

func TestBuildSuggestions(t *testing.T) {
	agents := []*domain.Agent{
		{Name: "claude", Role: "backend", Team: "builders", Status: domain.StatusReady},
		{Name: "codex", Role: "backend", Team: "builders", Status: domain.StatusReady},
		{Name: "opencode", Role: "reviewer", Team: "reviewers", Status: domain.StatusReady},
		{Name: "gone", Role: "reviewer", Status: domain.StatusStopped}, // terminal: name excluded
	}
	tasks := []*domain.Task{
		{ID: "task_open1", Status: domain.TaskAssigned},
		{ID: "task_done", Status: domain.TaskCompleted}, // completed: excluded
	}

	all := buildSuggestions(agents, tasks, "@")
	joined := strings.Join(all, " ")
	for _, want := range []string{"@all", "@claude", "@codex", "@opencode",
		"@team:builders", "@team:reviewers", "@role:backend", "@role:reviewer", "@task:task_open1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing suggestion %s in %v", want, all)
		}
	}
	if strings.Contains(joined, "@gone") || strings.Contains(joined, "task_done") {
		t.Fatalf("terminal agent / completed task leaked: %v", all)
	}

	// Prefix filtering matches both the full selector and the bare value:
	// "@rev" finds role:reviewer and team:reviewers.
	rev := strings.Join(buildSuggestions(agents, tasks, "@rev"), " ")
	if !strings.Contains(rev, "@role:reviewer") || !strings.Contains(rev, "@team:reviewers") {
		t.Fatalf("@rev should match role and team: %v", rev)
	}
	if strings.Contains(rev, "@claude") {
		t.Fatalf("@rev must not match claude: %v", rev)
	}
	// A completed selector followed by a space deactivates suggestions.
	if got := buildSuggestions(agents, tasks, "@claude hi"); got != nil {
		t.Fatalf("suggestions must deactivate after a space: %v", got)
	}
	// Non-@ input yields nothing.
	if got := buildSuggestions(agents, tasks, "/help"); got != nil {
		t.Fatalf("non-@ input must yield nothing: %v", got)
	}
}

func TestChatFilterChips(t *testing.T) {
	agents := []*domain.Agent{
		{Name: "claude", Status: domain.StatusReady},
		{Name: "codex", Status: domain.StatusReady},
		{Name: "claude/helper", Adapter: "observed", Status: domain.StatusCompleted},
	}
	opts := chatFilterOptions(agents)
	want := []string{"", "@all", "lead", "claude", "codex"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v (observed sub-agents excluded)", opts, want)
	}
	// Forward/backward stepping wraps.
	if got := chatFilterStep("", agents, true); got != "@all" {
		t.Fatalf("step forward from everything = %q", got)
	}
	if got := chatFilterStep("", agents, false); got != "codex" {
		t.Fatalf("step backward from everything should wrap = %q", got)
	}
	if got := chatFilterStep("codex", agents, true); got != "" {
		t.Fatalf("step forward from last should wrap = %q", got)
	}
	// Labels.
	if chatFilterLabel("") != "everything" || chatFilterLabel("@all") != "broadcasts" || chatFilterLabel("codex") != "codex" {
		t.Fatal("labels wrong")
	}
}

func TestMatchChatFilter(t *testing.T) {
	direct := &domain.Message{Sender: "lead", Recipient: "claude", Selector: "@claude"}
	bcast := &domain.Message{Sender: "codex", Recipient: "claude", Selector: "@all"}
	team := &domain.Message{Sender: "lead", Recipient: "opencode", Selector: "@team:reviewers"}

	if !matchChatFilter(direct, "") || !matchChatFilter(bcast, "") {
		t.Fatal("empty filter must match everything")
	}
	if matchChatFilter(direct, "@all") || !matchChatFilter(bcast, "@all") {
		t.Fatal("@all filter must match only broadcasts")
	}
	if !matchChatFilter(direct, "claude") || !matchChatFilter(bcast, "claude") || matchChatFilter(team, "claude") {
		t.Fatal("name filter must match sender or recipient")
	}
	if !matchChatFilter(direct, "lead") || matchChatFilter(bcast, "lead") {
		t.Fatal("lead filter must match lead traffic only")
	}
	if !matchChatFilter(team, "@team:reviewers") || matchChatFilter(direct, "@team:reviewers") {
		t.Fatal("selector filter must match exact selector")
	}
}

func TestComposerGrowsWithWrappedInput(t *testing.T) {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.SetWidth(20)
	ta.SetHeight(1)
	ta.MaxHeight = maxInputRows
	m := &model{input: ta}

	m.input.SetValue("hi")
	m.syncInputHeight()
	if got := m.input.Height(); got != 1 {
		t.Errorf("short input height = %d, want 1", got)
	}

	m.input.SetValue(strings.Repeat("wrap ", 40)) // wraps well past width 20
	m.syncInputHeight()
	if got := m.input.Height(); got < 2 {
		t.Errorf("wrapped input height = %d, want multi-line (>1)", got)
	}
	if got := m.input.Height(); got > maxInputRows {
		t.Errorf("input height = %d exceeds cap %d", got, maxInputRows)
	}

	m.input.SetValue("")
	m.syncInputHeight()
	if got := m.input.Height(); got != 1 {
		t.Errorf("cleared input height = %d, want 1", got)
	}
}
