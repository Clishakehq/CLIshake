package cli

import (
	"context"
	"fmt"

	"github.com/clishakehq/clishake/internal/selfupdate"
	"github.com/clishakehq/clishake/internal/ui"
)

// runDashboard opens (or creates) the project session, reconciles state,
// and starts the interactive dashboard.
func runDashboard() error {
	// Warm the release-check cache in the background while the user works,
	// so the upgrade notice (printed by PersistentPostRun on exit) is fresh.
	// Fire-and-forget: any failure just leaves the cache as-is.
	go selfupdate.Latest(context.Background())

	o, err := open()
	if err != nil {
		return err
	}
	defer o.Close()
	if _, err := o.EnsureSession(); err != nil {
		return err
	}
	report, err := o.Reconcile()
	if err != nil {
		return err
	}
	for _, r := range report {
		fmt.Println("reconcile: " + r)
	}
	return ui.Run(o)
}
