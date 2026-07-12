package tui

import (
	"strings"
	"testing"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
)

func spec() Spec { return Spec{Name: "opencode", Command: "opencode"} }

func TestDefaultsApplied(t *testing.T) {
	a := New(spec())
	if got := a.InterruptKeys(); len(got) != 1 || got[0] != "Escape" {
		t.Fatalf("InterruptKeys = %v", got)
	}
	if a.Name() != "opencode" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.InputMode() != adapter.InputSendKeys {
		t.Fatalf("InputMode = %q", a.InputMode())
	}
	if a.BriefsAtLaunch() {
		t.Fatal("generic TUI adapters must not claim launch-time briefing")
	}
}

func TestDetect(t *testing.T) {
	origLook, origVer := lookPath, runVersion
	defer func() { lookPath, runVersion = origLook, origVer }()

	lookPath = func(string) (string, error) { return "", strings.NewReader("").UnreadByte() }
	if ok, _, _ := New(spec()).Detect(); ok {
		t.Fatal("Detect should be false when binary missing")
	}
	lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	runVersion = func(string, ...string) (string, error) { return "opencode 0.6.3\nextra banner", nil }
	ok, v, _ := New(spec()).Detect()
	if !ok || v != "opencode 0.6.3" {
		t.Fatalf("Detect = %v %q", ok, v)
	}
}

func TestBuildLaunchOverrides(t *testing.T) {
	ag := &domain.Agent{Name: "oc", WorkDir: "/work", Config: map[string]string{
		"command": "/opt/opencode", "args": "--model gpt-5",
	}}
	ls, err := New(spec()).BuildLaunch(ag, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/opencode", "--model", "gpt-5"}
	if strings.Join(ls.Command, " ") != strings.Join(want, " ") {
		t.Fatalf("Command = %v", ls.Command)
	}
	if ls.WorkDir != "/work" {
		t.Fatalf("WorkDir = %q", ls.WorkDir)
	}
}

func TestFormatInputPrefix(t *testing.T) {
	got, _ := New(spec()).FormatInput(&domain.Agent{}, domain.Message{Sender: "lead", Body: "hi"})
	if got != "[clishake message from lead] hi" {
		t.Fatalf("FormatInput = %q", got)
	}
}

func TestDetectReadyPromptGlyphHeuristic(t *testing.T) {
	a := New(spec())
	ag := &domain.Agent{Config: map[string]string{}}

	// Prompt-glyph lines count as ready…
	for _, chunk := range []string{
		"banner\n\x1b[2m> \x1b[0m\n",  // bare >
		"opencode v1\n › type here\n", // ›
		"❯ \n",                        // ❯ composer
	} {
		if !a.DetectReady(ag, chunk) {
			t.Errorf("should be ready: %q", chunk)
		}
	}
	// …but numbered menu cursors and glyphless output do not.
	for _, chunk := range []string{
		"Do you trust this folder?\n❯ 1. Yes, I trust it\n  2. No\n",
		"> 1. accept all\n",
		"just a banner loading\n",
		"",
	} {
		if a.DetectReady(ag, chunk) {
			t.Errorf("should NOT be ready: %q", chunk)
		}
	}
}

func TestDetectReadyConfigMarker(t *testing.T) {
	a := New(spec())
	ag := &domain.Agent{Config: map[string]string{"ready_marker": "Ask anything"}}
	if !a.DetectReady(ag, "banner\nAsk anything about the codebase\n") {
		t.Fatal("configured ready_marker should trigger readiness")
	}
}

func TestSpecReadyMarkers(t *testing.T) {
	a := New(Spec{Name: "x", Command: "x", ReadyMarkers: []string{"composer ready"}})
	ag := &domain.Agent{}
	if !a.DetectReady(ag, "...composer ready...") {
		t.Fatal("spec marker should trigger readiness")
	}
}

func TestCheckHealth(t *testing.T) {
	a := New(spec())
	if a.CheckHealth(&domain.Agent{}, false, 0) != adapter.HealthUnknown {
		t.Fatal("dead process should be unknown health")
	}
	if a.CheckHealth(&domain.Agent{}, true, 9999) != adapter.HealthOK {
		t.Fatal("quiet interactive CLIs are still ok")
	}
}

func TestDetectReadyDialogVetoesEvenWithComposerVisible(t *testing.T) {
	a := New(spec())
	ag := &domain.Agent{Config: map[string]string{}}
	// Live Copilot CLI capture shape: composer ❯ visible AND a trust
	// dialog overlay with its own ❯ 1. cursor — must NOT be ready.
	screen := "❯\n────\n│ ❯ 1. Yes                       │\n│   2. Yes, and remember          │\n"
	if a.DetectReady(ag, screen) {
		t.Fatal("dialog overlay must veto readiness even when composer glyph is visible")
	}
	// Same veto applies when a ready_marker would otherwise match.
	ag.Config["ready_marker"] = "remember"
	if a.DetectReady(ag, screen) {
		t.Fatal("dialog veto must outrank config markers")
	}
}

func TestBuildLaunchModelSelection(t *testing.T) {
	join := func(ag *domain.Agent) string {
		ls, err := New(spec()).BuildLaunch(ag, "/proj")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(ls.Command, " ")
	}
	// default flag is --model
	if got := join(&domain.Agent{Name: "a", WorkDir: "/proj", Config: map[string]string{"model": "sonnet"}}); !strings.Contains(got, "--model sonnet") {
		t.Fatalf("default model flag: %q", got)
	}
	// model_flag overrides the flag name
	if got := join(&domain.Agent{Name: "a", WorkDir: "/proj", Config: map[string]string{"model": "x", "model_flag": "-m"}}); !strings.Contains(got, "-m x") {
		t.Fatalf("custom model flag: %q", got)
	}
	// empty model_flag disables model selection for a harness without one
	if got := join(&domain.Agent{Name: "a", WorkDir: "/proj", Config: map[string]string{"model": "x", "model_flag": ""}}); strings.Contains(got, " x") {
		t.Fatalf("empty model_flag should omit the model: %q", got)
	}
}

func TestBuildLaunchPermissionProfiles(t *testing.T) {
	sp := spec()
	sp.PermissionFlags = map[string]string{"full": "--allow-all-tools"}
	join := func(cfg map[string]string) string {
		ls, _ := New(sp).BuildLaunch(&domain.Agent{Name: "a", WorkDir: "/proj", Config: cfg}, "/proj")
		return strings.Join(ls.Command, " ")
	}
	if got := join(map[string]string{"permissions": "full"}); !strings.Contains(got, "--allow-all-tools") {
		t.Errorf("spec PermissionFlags[full]: %q", got)
	}
	// unknown profile for this harness → no-op (honest)
	if got := join(map[string]string{"permissions": "auto"}); strings.Contains(got, "--allow-all-tools") {
		t.Errorf("auto not mapped for this spec, should be no-op: %q", got)
	}
	// per-agent perm_<profile> config overrides
	if got := join(map[string]string{"permissions": "auto", "perm_auto": "--yolo"}); !strings.Contains(got, "--yolo") {
		t.Errorf("perm_auto override: %q", got)
	}
}
