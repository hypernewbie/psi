package tools

import (
	"os"
	"testing"

	"github.com/hypernewbie/psi/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m.Run)) }
