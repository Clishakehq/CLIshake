// Package brand holds CLIshake's terminal identity: the wordmark, the
// handshake motif, and the tagline.
//
// The handshake is in the colors, not a picture: the wordmark's left half
// renders in one agent color and the right half in another, joined in the
// middle — two parties, one clasp. The motif line makes it explicit: two
// prompt glyphs (two CLIs) reaching toward a shared grip.
package brand

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Tagline is the one-line product description.
const Tagline = "the terminal coordination layer for collaborating coding agents"

// wordmark is "CLIshake" in figlet-standard style. Keep every line the
// same visual width so the two-tone split stays vertical.
var wordmark = []string{
	"  ____ _     ___     _           _        ",
	" / ___| |   |_ _|___| |__   __ _| | _____ ",
	"| |   | |    | |/ __| '_ \\ / _` | |/ / _ \\",
	"| |___| |___ | |\\__ \\ | | | (_| |   <  __/",
	" \\____|_____|___|___/_| |_|\\__,_|_|\\_\\___|",
}

// motif is the handshake: two prompts meeting at a clasp.
const motif = `❯❯ ───────────────┤├─────────────── ❮❮`

var (
	leftTone  = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))  // cyan — one agent
	rightTone = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // pink — the other
	claspTone = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	dimTone   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// Banner returns the plain (uncolored) banner: wordmark, motif, tagline.
// Use for help output and anywhere ANSI may not be welcome.
func Banner(version string) string {
	var b strings.Builder
	for _, l := range wordmark {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n " + motif + "\n")
	b.WriteString(" " + Tagline)
	if version != "" {
		b.WriteString("  ·  v" + version)
	}
	b.WriteString("\n")
	return b.String()
}

// ColorBanner returns the two-tone banner for interactive terminals.
func ColorBanner(version string) string {
	var b strings.Builder
	for _, l := range wordmark {
		b.WriteString(splitTone(l) + "\n")
	}
	b.WriteString("\n " + motifColored() + "\n")
	line := " " + Tagline
	if version != "" {
		line += "  ·  v" + version
	}
	b.WriteString(dimTone.Render(line) + "\n")
	return b.String()
}

// splitTone colors the left half of a line cyan and the right half pink —
// the handshake, applied to the wordmark itself.
func splitTone(line string) string {
	r := []rune(line)
	mid := len(r) / 2
	return leftTone.Render(string(r[:mid])) + rightTone.Render(string(r[mid:]))
}

// motifColored renders the two prompts in their agent tones and the clasp
// bright: two CLIs, one grip.
func motifColored() string {
	parts := strings.SplitN(motif, "┤├", 2)
	if len(parts) != 2 {
		return motif
	}
	return leftTone.Render(parts[0]) + claspTone.Render("┤├") + rightTone.Render(parts[1])
}
