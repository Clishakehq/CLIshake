package cli

import (
	"strings"
	"testing"
)

func TestStripANSIRemovesTerminalModeSequences(t *testing.T) {
	// The exact sequences captured from a real Claude Code pane log that
	// reprogrammed the viewer's terminal (mouse tracking, alt screen,
	// bracketed paste, kitty keyboard protocol, OSC title).
	in := "\x1b7\x1b[r\x1b8\x1b[?25h\x1b[?25l\x1b[?2004h\x1b[?1049h" +
		"\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h" +
		"\x1b[<u\x1b[>1u\x1b[>4;2m\x1b[>0q\x1b[c" +
		"\x1b]0;Claude Code\x07" +
		"hello\x1b[38;5;174m world\x1b[0m\r\n" +
		"\x1b[2J\x1b[H" +
		"second line\n"
	got := StripANSI(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape bytes survived: %q", got)
	}
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "second line") {
		t.Fatalf("visible text lost: %q", got)
	}
}

func TestStripANSIDropsBlankLinesAndKeepsTabs(t *testing.T) {
	got := StripANSI("a\tb\n\x1b[2K\n\n\nreal\n")
	if got != "a\tb\nreal" {
		t.Fatalf("got %q", got)
	}
}
