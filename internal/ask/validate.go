package ask

import (
	"fmt"
	"strings"

	"github.com/clishakehq/clishake/internal/domain"
)

// commandSpec describes one whitelisted top-level command shape.
type commandSpec struct {
	// subcommands, if non-empty, lists the allowed second-token values.
	subcommands []string
	// subRequired requires argv[1] to be present and match subcommands.
	// When false and subcommands is non-empty, the subcommand is optional
	// (e.g. "approvals" alone lists requests; "approvals grant <id>" decides
	// one).
	subRequired bool
	// flags, keyed by subcommand ("" for commands with no subcommand),
	// lists the allowed --flag names. A command/subcommand pair absent
	// from this map has no flag restriction.
	flags map[string][]string
}

// whitelist is the exhaustive, data-driven set of clishake command shapes
// the ask package may translate natural language into. Extending clishake's
// vocabulary means adding an entry here (and to whitelistDoc for the
// prompt) — nothing else in this package needs to change.
var whitelist = map[string]commandSpec{
	"agent": {
		subcommands: []string{"add", "start", "stop", "restart", "remove"},
		subRequired: true,
		flags: map[string][]string{
			"add": {"--adapter", "--role", "--task", "--team", "--no-start", "--model", "--permissions"},
		},
	},
	"send":      {},
	"broadcast": {},
	"task": {
		subcommands: []string{"create", "assign", "update"},
		subRequired: true,
		flags: map[string][]string{
			"create": {"--title", "--description", "--assign", "--priority", "--depends-on"},
			"update": {"--status", "--summary"},
		},
	},
	"tasks":    {},
	"agents":   {},
	"status":   {},
	"messages": {},
	"events":   {},
	"note":     {},
	"approvals": {
		subcommands: []string{"grant", "deny"},
		subRequired: false,
	},
}

// whitelistDoc renders the whitelist as human-readable usage lines for
// BuildPrompt.
func whitelistDoc() []string {
	return []string{
		`agent add <name> [--adapter NAME] [--model NAME] [--permissions default|auto|full|plan] [--role ROLE] [--task "..."] [--team NAME] [--no-start]`,
		"agent start <name>",
		"agent stop <name>",
		"agent restart <name>",
		"agent remove <name>",
		`send @selector "message"`,
		`broadcast "message"`,
		`task create --title "..." [--description "..."] [--assign NAME] [--priority N] [--depends-on ID,ID,...]`,
		"task assign <task-id> <agent-name>",
		`task update <task-id> --status STATUS [--summary "..."]`,
		"tasks",
		"agents",
		"status",
		"messages",
		"events",
		`note "text"`,
		"approvals",
		"approvals grant <id>",
		"approvals deny <id>",
	}
}

// Validate checks every command in the plan against the whitelist. It
// returns a descriptive error naming the offending command otherwise.
func Validate(p Plan) error {
	if len(p.Commands) == 0 {
		return fmt.Errorf("plan has no commands")
	}
	for i, argv := range p.Commands {
		if err := validateOne(argv); err != nil {
			return fmt.Errorf("command %d (%q): %w", i+1, strings.Join(argv, " "), err)
		}
	}
	return nil
}

func validateOne(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	for _, tok := range argv {
		if strings.ContainsAny(tok, "\n\r") {
			return fmt.Errorf("token %q contains a newline", tok)
		}
		if tok == "--kill-session" {
			return fmt.Errorf("--kill-session is not permitted")
		}
	}

	spec, ok := whitelist[argv[0]]
	if !ok {
		return fmt.Errorf("command %q is not in the whitelist", argv[0])
	}

	sub := ""
	rest := argv[1:]
	if len(spec.subcommands) > 0 {
		if len(argv) < 2 || strings.HasPrefix(argv[1], "-") {
			if spec.subRequired {
				return fmt.Errorf("%q requires a subcommand (one of %s)", argv[0], strings.Join(spec.subcommands, "|"))
			}
		} else {
			sub = argv[1]
			if !contains(spec.subcommands, sub) {
				return fmt.Errorf("%q is not a valid subcommand of %q (must be one of %s)", sub, argv[0], strings.Join(spec.subcommands, "|"))
			}
			rest = argv[2:]
		}
	}

	// "agent add <name>" mints a new, addressable name. Enforce the same rule
	// the orchestrator does, here, so an invalid model-chosen name (e.g. a
	// capitalized "Jean-Pierre") is reported clearly before execution rather
	// than surfacing as a bare "exit status 1".
	if argv[0] == "agent" && sub == "add" {
		if len(argv) < 3 || strings.HasPrefix(argv[2], "-") {
			return fmt.Errorf("agent add requires an agent name as its first argument")
		}
		if err := domain.ValidAgentName(argv[2]); err != nil {
			return fmt.Errorf("%w (lowercase letters, digits, '-', '_' only)", err)
		}
	}

	allowed, restricted := spec.flags[sub]
	if !restricted {
		return nil
	}
	for _, tok := range rest {
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name := tok
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			name = tok[:eq]
		}
		if !contains(allowed, name) {
			label := strings.TrimSpace(argv[0] + " " + sub)
			return fmt.Errorf("flag %q is not allowed for %q", name, label)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
