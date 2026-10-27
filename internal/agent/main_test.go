package agent

import (
	"os"
	"testing"

	"psi/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m.Run)) }
