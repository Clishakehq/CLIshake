// Package ansi strips terminal escape sequences and control characters
// from captured output so it can be matched or displayed safely.
package ansi

import (
	"regexp"
	"strings"
)

var (
	// CSI (\e[...X), OSC (\e]...BEL or \e]...\e\), DCS/SOS/PM/APC
	// (\eP/\eX/\e^/\e_ ... \e\), charset selection (\e(X etc.), and
	// remaining single-char escapes.
	reANSI = regexp.MustCompile(
		`\x1b\[[0-9;?<>=!]*[a-zA-Z@^_\x60~{|}]` + // CSI
			`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC
			`|\x1b[PX^_][^\x1b]*\x1b\\` + // DCS/SOS/PM/APC
			`|\x1b[()*+][0-9A-Za-z]` + // charset
			`|\x1b.`) // any leftover ESC pair
	reCtrl = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

	// reLiteralEscape matches CSI escape sequences that reached us as LITERAL
	// text — e.g. a status line that printed the characters "\033[0;34m" (or
	// "\e[..m" / "\x1b[..m") instead of a real ESC byte. tmux capture-pane
	// already renders real escapes, so only these textual leftovers survive
	// into a rendered screen.
	reLiteralEscape = regexp.MustCompile(`(?:\\033|\\x1b|\\e)\[[0-9;?]*[A-Za-z]`)
)

// StripLiteralEscapes removes textual escape-code representations (see
// reLiteralEscape) from already-rendered plain text, so a glance/preview of
// an agent's screen isn't cluttered by an agent's mis-escaped status line.
// Unlike Strip, it does not touch real ESC bytes (there are none in rendered
// capture-pane output).
func StripLiteralEscapes(s string) string {
	return reLiteralEscape.ReplaceAllString(s, "")
}

// Strip removes escape sequences and non-printing control characters
// (keeping newlines and tabs), normalizing \r\n and \r to \n.
func Strip(s string) string {
	s = reANSI.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return reCtrl.ReplaceAllString(s, "")
}
