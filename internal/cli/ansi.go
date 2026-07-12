package cli

import (
	"strings"

	"github.com/clishakehq/clishake/internal/ansi"
)

// TUI harnesses write raw terminal control data (alternate screen, mouse
// tracking, keyboard protocols) into their piped output logs. Printing that
// verbatim reprograms the *viewer's* terminal — e.g. `\e[?1003h` turns on
// mouse reporting, which keeps spewing input escapes until reset. Anything
// shown by default must be stripped first.

// StripANSI removes escape sequences and non-printing control characters
// (keeping newlines and tabs), then drops lines that end up empty so
// redraw-heavy TUI logs stay readable.
func StripANSI(s string) string {
	s = ansi.Strip(s)
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
