// Package app owns startup, command dispatch, and the active session lifecycle.
package app

import (
	"flag"
	"fmt"
)

// Run dispatches CLI commands. Full implementation arrives in Phase 8/10;
// this stub establishes the approved CLI boundary (PLAN Phase 0 Step 5).
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("psi", flag.ContinueOnError)
	var (
		showHelp    bool
		showVersion bool
		continue_   bool
		resume      bool
		sessionPath string
		provider    string
		model       string
		thinking    string
		sessionDir  string
		tools       string
		noTools     bool
		noSession   bool
	)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showVersion, "version", false, "show version")
	fs.BoolVar(&continue_, "continue", false, "continue most recent session")
	fs.BoolVar(&resume, "resume", false, "resume with selection")
	fs.StringVar(&sessionPath, "session", "", "open explicit session path")
	fs.StringVar(&provider, "provider", "", "provider")
	fs.StringVar(&model, "model", "", "model")
	fs.StringVar(&thinking, "thinking", "", "thinking level")
	fs.StringVar(&sessionDir, "session-dir", "", "session directory")
	fs.StringVar(&tools, "tools", "", "tool list")
	fs.BoolVar(&noTools, "no-tools", false, "disable tools")
	fs.BoolVar(&noSession, "no-session", false, "in-memory session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if showVersion {
		fmt.Printf("psi %s\n", version)
		return nil
	}
	if showHelp || (fs.NArg() == 0 && !continue_ && !resume && sessionPath == "" && !noSession) {
		// Minimal startup status (Phase 10 Step 86 shape, without provider calls).
		fmt.Printf("psi %s — minimal Go coding agent (inline, Pi-compatible sessions)\n", version)
		fs.PrintDefaults()
		return nil
	}
	return fmt.Errorf("interactive terminal arrives in Phase 10 (M6); storage/config/provider/tools/agent land first")
}
