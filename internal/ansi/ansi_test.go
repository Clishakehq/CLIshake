package ansi

import "testing"

func TestStrip(t *testing.T) {
	// Real ESC bytes (\x1b) are removed; text is preserved.
	in := "\x1b[0;34mhello\x1b[0m world\r\n"
	got := Strip(in)
	if got != "hello world\n" {
		t.Errorf("Strip = %q, want %q", got, "hello world\n")
	}
}

func TestStripLiteralEscapes(t *testing.T) {
	// The exact shape a mis-escaped status line prints (literal backslash-033).
	in := `~/Code/Claude/junk \033[0;34mOpus 4.8 (1M context)\033[0m \033[0;32mctx:9%\033[0m`
	want := `~/Code/Claude/junk Opus 4.8 (1M context) ctx:9%`
	if got := StripLiteralEscapes(in); got != want {
		t.Errorf("StripLiteralEscapes = %q, want %q", got, want)
	}

	// \e[..m and \x1b[..m textual forms too.
	if got := StripLiteralEscapes(`a\e[1mb\x1b[0mc`); got != "abc" {
		t.Errorf("StripLiteralEscapes variants = %q, want %q", got, "abc")
	}

	// Ordinary text (and a lone backslash) is untouched.
	if got := StripLiteralEscapes(`path C:\Users x`); got != `path C:\Users x` {
		t.Errorf("StripLiteralEscapes must not touch ordinary text, got %q", got)
	}
}
