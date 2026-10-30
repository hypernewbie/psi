package leakcheck

import (
	"testing"

	"go.uber.org/goleak"
)

// DefaultOptions returns leak options that ignore known runtime or third-party background goroutines.
func DefaultOptions() []goleak.Option {
	return []goleak.Option{}
}

// Verify runs a goroutine leak check for a single test.
func Verify(t *testing.T, opts ...goleak.Option) {
	t.Helper()
	allOpts := append(DefaultOptions(), opts...)
	goleak.VerifyNone(t, allOpts...)
}

// VerifyTestMain runs TestMain with goleak verification and exits.
func VerifyTestMain(m *testing.M, opts ...goleak.Option) {
	allOpts := append(DefaultOptions(), opts...)
	goleak.VerifyTestMain(m, allOpts...)
}
