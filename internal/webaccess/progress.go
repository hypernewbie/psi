package webaccess

import "github.com/hypernewbie/psi/internal/event"

// reportWebProgress sends one bounded display-only snapshot. A nil callback
// (RPC, tests without a host) is a no-op, and the callback itself is
// non-blocking in the agent layer.
func reportWebProgress(report func(event.WebProgress), phase string, completed, total int, label string) {
	if report == nil {
		return
	}
	report(event.WebProgress{Phase: phase, Label: label, Completed: completed, Total: total})
}
