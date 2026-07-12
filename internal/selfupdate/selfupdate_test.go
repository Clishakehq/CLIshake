package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.1.0", "0.1.1", true},
		{"0.1.0", "1.0.0", true},
		{"v0.1.0", "v0.2.0", true},   // "v" prefix tolerated on both sides
		{"0.1.0", "v0.1.0", false},   // equal
		{"0.2.0", "0.1.0", false},    // latest older
		{"1.0.0", "0.9.9", false},    // major beats minor/patch
		{"0.1.0-dev", "0.1.0", true}, // pre-release is older than its release
		{"0.1.0", "0.1.0-dev", false},
		{"0.1.0-dev", "0.1.0-dev", false}, // equal pre-releases
		{"", "0.1.0", true},               // unknown current treated as 0.0.0
		{"0.1.0", "", false},              // no latest known
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	maj, min, pat, pre := parse("v1.2.3-rc1")
	if maj != 1 || min != 2 || pat != 3 || !pre {
		t.Errorf("parse(v1.2.3-rc1) = %d,%d,%d,%v", maj, min, pat, pre)
	}
	maj, min, pat, pre = parse(" 0.1.0 ")
	if maj != 0 || min != 1 || pat != 0 || pre {
		t.Errorf("parse(0.1.0) = %d,%d,%d,%v", maj, min, pat, pre)
	}
}

func TestNotice(t *testing.T) {
	if got := Notice("0.1.0", "0.2.0"); got == "" {
		t.Error("expected a notice when a newer release exists")
	}
	if got := Notice("0.2.0", "0.2.0"); got != "" {
		t.Errorf("expected no notice at latest, got %q", got)
	}
	if got := Notice("0.1.0", ""); got != "" {
		t.Errorf("expected no notice with unknown latest, got %q", got)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	// Point the cache at a temp dir so we never touch the real one.
	// os.UserCacheDir honors XDG_CACHE_HOME on Linux and $HOME/Library/Caches
	// on macOS, so override both to keep the test isolated on either OS.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)
	p, err := cachePath()
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := cache{CheckedAt: time.Unix(1_700_000_000, 0), Latest: "v9.9.9"}
	b, _ := json.Marshal(want)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := loadCache()
	if got.Latest != want.Latest {
		t.Errorf("loadCache().Latest = %q, want %q", got.Latest, want.Latest)
	}
}

func TestDisabled(t *testing.T) {
	t.Setenv("CLISHAKE_NO_UPDATE_CHECK", "1")
	if CachedLatest() != "" {
		t.Error("CachedLatest should be empty when checks are disabled")
	}
	if Latest(nil) != "" {
		t.Error("Latest should be empty when checks are disabled")
	}
}
