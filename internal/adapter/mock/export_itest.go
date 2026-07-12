package mock

// SetSelfExeForTesting overrides the executable path BuildLaunch uses to
// launch the mock agent subprocess, returning a restore function. It exists
// solely so integration tests in OTHER packages (e.g.
// internal/orchestrator's real-tmux integration test) can point the mock
// adapter at a prebuilt clishake binary instead of os.Executable(), which
// under `go test` resolves to the test binary itself and has no
// "mock-agent" subcommand. Not used by, or intended for, production code.
func SetSelfExeForTesting(path string) (restore func()) {
	orig := selfExe
	selfExe = func() (string, error) { return path, nil }
	return func() { selfExe = orig }
}
