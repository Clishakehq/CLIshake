// Package selfupdate checks GitHub for newer clishake releases and tells
// the user about them. The check is cached (once per day), network errors
// never surface, and it can be disabled with CLISHAKE_NO_UPDATE_CHECK=1.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub owner/name the release check queries.
const Repo = "clishakehq/clishake"

// checkInterval is the minimum time between network checks.
const checkInterval = 24 * time.Hour

// disabled reports whether update checks are turned off.
func disabled() bool {
	v := os.Getenv("CLISHAKE_NO_UPDATE_CHECK")
	return v == "1" || v == "true"
}

// cache is the on-disk record of the last check.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"` // latest release tag, e.g. "v0.2.0"
}

func cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clishake", "update.json"), nil
}

func loadCache() cache {
	var c cache
	p, err := cachePath()
	if err != nil {
		return c
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

func saveCache(c cache) {
	p, err := cachePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// Newer reports whether latest is a strictly greater semantic version than
// current. Both may carry a leading "v". A pre-release build (e.g.
// "0.1.0-dev") of the same numbers is considered OLDER than the release.
func Newer(current, latest string) bool {
	cMaj, cMin, cPat, cPre := parse(current)
	lMaj, lMin, lPat, lPre := parse(latest)
	for _, d := range [][2]int{{cMaj, lMaj}, {cMin, lMin}, {cPat, lPat}} {
		if d[1] != d[0] {
			return d[1] > d[0]
		}
	}
	// Same numbers: a non-pre-release beats a pre-release.
	return cPre && !lPre
}

// parse splits "v1.2.3-rc1" into (1,2,3,isPreRelease).
func parse(v string) (maj, min, pat int, pre bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		pre = true
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	nums := [3]*int{&maj, &min, &pat}
	for i := 0; i < len(parts) && i < 3; i++ {
		*nums[i], _ = strconv.Atoi(parts[i])
	}
	return maj, min, pat, pre
}

// Latest returns the newest release tag known to us. It uses the cache when
// it is fresh (< checkInterval); otherwise it queries GitHub (short timeout)
// and refreshes the cache. Any failure returns whatever the cache holds
// (possibly ""), never an error. Disabled checks return "".
func Latest(ctx context.Context) string {
	if disabled() {
		return ""
	}
	c := loadCache()
	if c.Latest != "" && time.Since(c.CheckedAt) < checkInterval {
		return c.Latest
	}
	tag, err := fetchLatest(ctx)
	if err != nil || tag == "" {
		return c.Latest // stale-but-usable, or ""
	}
	saveCache(cache{CheckedAt: time.Now(), Latest: tag})
	return tag
}

// CachedLatest returns the last-known latest tag WITHOUT any network call —
// for showing a notice cheaply after an arbitrary command.
func CachedLatest() string {
	if disabled() {
		return ""
	}
	return loadCache().Latest
}

// fetchLatest queries the GitHub releases API. Requires the repo to be
// public (or an authenticated request); a private repo returns 404, which
// surfaces here as an error and is swallowed by the caller.
func fetchLatest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	url := "https://api.github.com/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "clishake")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.TagName, nil
}

// Notice returns a one-line upgrade message if `latest` is newer than
// `current`, otherwise "". Pass CachedLatest() or Latest() as `latest`.
func Notice(current, latest string) string {
	if latest == "" || !Newer(current, latest) {
		return ""
	}
	return fmt.Sprintf("clishake %s is available (you have %s) — run `clishake update`", latest, current)
}
