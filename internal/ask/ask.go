// Package ask implements clishake's natural-language front-end: it turns a
// team lead's plain-English request into a small, validated plan of
// clishake CLI commands using a locally installed AI CLI (claude or codex)
// as the translator. The model never runs anything directly — it only
// proposes argv lists, which Validate checks against a fixed whitelist
// before the caller ever executes them.
package ask

import (
	"fmt"
	"strings"
)

// Plan is the model's proposed translation of the lead's intent.
type Plan struct {
	Commands    [][]string `json:"commands"`    // argv per command, WITHOUT the leading "clishake"
	Explanation string     `json:"explanation"` // one-paragraph rationale shown to the user
}

// SessionContext is a compact snapshot given to the model.
type SessionContext struct {
	ProjectDir string
	Agents     []string // e.g. "claude (role backend, adapter claude-code, status ready)"
	Tasks      []string // e.g. "task_ab12 [assigned→claude] Fix login bug"
	Messages   []string // recent, e.g. "lead → claude: ..."
	Adapters   []string // available adapter names
}

// BuildPrompt renders the full instruction prompt for the model: the role,
// the exact whitelist of allowed command shapes with their flags, the
// session context, the user query, and strict output instructions.
func BuildPrompt(sc SessionContext, query string) string {
	var b strings.Builder

	b.WriteString("You are the natural-language front-end for clishake, a CLI that orchestrates coding agents.\n")
	b.WriteString("Your job is to translate the team lead's request into clishake CLI commands.\n\n")

	b.WriteString("ALLOWED COMMAND SHAPES — this is an EXHAUSTIVE whitelist. Do not invent commands,\n")
	b.WriteString("subcommands, or flags outside it. If the request cannot be satisfied with these\n")
	b.WriteString("shapes, do the closest reasonable safe subset and explain the gap.\n\n")
	for _, line := range whitelistDoc() {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")

	b.WriteString("NAMING RULES:\n")
	b.WriteString("  An agent name from \"agent add <name>\" may use letters (any case),\n")
	b.WriteString("  digits, '-', and '_' — e.g. \"reviewer\", \"codex-2\", \"Jean-Pierre\". Use the\n")
	b.WriteString("  exact name the lead asked for. No spaces (join with '-' or '_') or other\n")
	b.WriteString("  punctuation, and never name an agent lead, all, team, or role.\n\n")

	b.WriteString("SESSION CONTEXT:\n")
	fmt.Fprintf(&b, "  project directory: %s\n", sc.ProjectDir)
	writeList(&b, "agents", sc.Agents)
	writeList(&b, "tasks", sc.Tasks)
	writeList(&b, "recent messages", sc.Messages)
	writeList(&b, "available adapters", sc.Adapters)
	b.WriteString("\n")

	b.WriteString("EXAMPLES:\n\n")
	b.WriteString("Request: \"add a claude-code agent named claude to fix the login bug\"\n")
	b.WriteString(`Response: {"commands": [["agent","add","claude","--adapter","claude-code","--role","backend","--task","Fix the login bug"]], "explanation": "Registers and starts a new agent named claude using the claude-code adapter, with an initial task describing the login bug fix."}` + "\n\n")

	b.WriteString("Request: \"tell everyone to run the test suite before lunch\"\n")
	b.WriteString(`Response: {"commands": [["broadcast","Please run the test suite before lunch"]], "explanation": "Broadcasts a reminder to every live agent to run the test suite."}` + "\n\n")

	b.WriteString("Request: \"create a task to refactor auth, assign it to claude, and mark task_ab12 as done\"\n")
	b.WriteString(`Response: {"commands": [["task","create","--title","Refactor auth","--assign","claude"],["task","update","task_ab12","--status","completed"]], "explanation": "Creates a new task for the auth refactor assigned to claude, and marks the existing task_ab12 completed."}` + "\n\n")

	fmt.Fprintf(&b, "TEAM LEAD REQUEST:\n%s\n\n", query)

	b.WriteString("OUTPUT INSTRUCTIONS:\n")
	b.WriteString("Respond with a single JSON object and nothing else: no prose before or after it,\n")
	b.WriteString("no markdown code fences, no commentary.\n")
	b.WriteString(`The JSON object must have exactly this shape: {"commands": [["<argv0>", "<argv1>", ...], ...], "explanation": "<one paragraph>"}.` + "\n")
	b.WriteString("Each entry in \"commands\" is the argv for one clishake invocation, WITHOUT the leading\n")
	b.WriteString("\"clishake\" token, and MUST match one of the allowed command shapes above exactly.\n")

	return b.String()
}

func writeList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(b, "  %s: (none)\n", label)
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, it := range items {
		fmt.Fprintf(b, "    - %s\n", it)
	}
}
