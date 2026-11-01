package app

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"psi/internal/ac"
	"psi/internal/agent"
	"psi/internal/terminal"
)

func (r *runtime) handleACCommand(args string, out io.Writer, compact func(string), cancelTimer func(), timerPending func() bool, rescheduleTimer func()) {
	parts := strings.Fields(strings.TrimSpace(args))
	command := "help"
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}
	value := ""
	if len(parts) > 1 {
		value = strings.Join(parts[1:], " ")
	}
	report := func(message string) { fmt.Fprintln(out, terminal.Sanitize(message)) }

	switch command {
	case "help", "status":
		if command == "help" {
			report(r.acHelpText())
		} else {
			report(r.acStatusText())
		}
	case "compact":
		compact(value)
	case "threshold":
		switch strings.ToLower(value) {
		case "":
			report(r.acStatusText())
		case "default":
			target := ac.DefaultTargetPercent
			r.ac.SetTargetPercent(&target)
			report("Adaptive target reset to 50% or 230,000 tokens, whichever is lower.")
		case "off", "null", "none":
			r.ac.SetTargetPercent(nil)
			report("Adaptive percentage target disabled for this session; growth forecasting still uses psi's threshold.")
		default:
			target, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(target) || math.IsInf(target, 0) || target <= 0 || target > 100 {
				report("Usage: /ac threshold <default|1-100|off>")
				return
			}
			r.ac.SetTargetPercent(&target)
			report(fmt.Sprintf("Adaptive compaction target set to %s%%, capped at 230,000 tokens, for this session.", strconv.FormatFloat(target, 'f', -1, 64)))
		}
	case "end-turn":
		if value == "" {
			report(r.acStatusText())
			return
		}
		if strings.EqualFold(value, "default") {
			threshold := ac.EndOfTurnThreshold{Unit: "percent", Value: ac.DefaultEndTurnPercent}
			r.ac.SetEndOfTurnThreshold(&threshold, true)
			report("End-of-turn compaction reset to 200,000 tokens or 40%, whichever is higher.")
			return
		}
		threshold, valid := ac.ParseEndOfTurnThreshold(value)
		if !valid {
			report("Usage: /ac end-turn <default|off|token-count|percent%>")
			return
		}
		r.ac.SetEndOfTurnThreshold(threshold, false)
		if threshold == nil {
			cancelTimer()
			report("End-of-turn compaction disabled for this session.")
		} else {
			report("End-of-turn compaction threshold set to " + formatACThreshold(threshold, 0, false) + ".")
		}
	case "end-turn-delay":
		if value == "" {
			report(r.acStatusText())
			return
		}
		wasPending := timerPending()
		delay, valid := ac.ParseEndOfTurnDelay(value)
		if !valid {
			report("End-of-turn delay must be between 0 and 35,791.39 minutes.")
			return
		}
		cancelTimer()
		r.ac.SetEndOfTurnDelay(delay)
		if delay == 0 {
			report("End-of-turn compaction delay set to 0 minutes (immediate).")
		} else {
			report(fmt.Sprintf("End-of-turn compaction delay set to %s minute(s).", strconv.FormatFloat(delay.Minutes(), 'f', -1, 64)))
		}
		if wasPending {
			rescheduleTimer()
		}
	case "compactor":
		switch strings.ToLower(value) {
		case "":
			report(r.acStatusText())
		case "clear":
			r.ac.SetCompactorModel("")
			report("Compactor cleared; compaction will use the active model.")
		case "current":
			if r.ov.Provider == "" || r.ov.Model == "" {
				report("No active model is available to save as the compactor.")
				return
			}
			model := r.ov.Provider + "/" + r.ov.Model
			r.ac.SetCompactorModel(model)
			report("Compactor set to " + model + " for this session.")
		default:
			report("Usage: /ac compactor <current|clear>")
		}
	case "enable":
		r.ac.SetEnabled(true)
		report("Automatic compaction enabled.")
	case "disable":
		r.ac.SetEnabled(false)
		cancelTimer()
		report("Automatic compaction disabled. psi's native compaction remains available.")
	default:
		report("Unknown /ac command: " + command + "\n\n" + r.acHelpText())
	}
}

func (r *runtime) acHelpText() string {
	return strings.Join([]string{
		"psi-ac: adaptive context compaction controls",
		"",
		"Usage: /ac [help] or /ac <command> [arguments]",
		"/autocompact is an alias for /ac.",
		"/ac compact [focus]       compact now; while busy, queue compaction and resume afterward",
		"/ac status                show thresholds, growth forecast, and last compaction",
		"/ac threshold <default|1-100|off> set the predictive target",
		"/ac end-turn <default|off|token-count|percent%> set final-run threshold",
		"/ac end-turn-delay <minutes> wait before final-run compaction; default 10, zero is immediate",
		"/ac compactor current|clear select or clear the summary model",
		"/ac enable | disable      toggle proactive compaction",
		"",
		"Defaults: adaptive min(230K, 50%); end-turn max(200K, 40%) after 10 minutes.",
		"Settings are session-local. Pi's configured compaction reserve remains the safety threshold.",
		"",
		r.acStatusText(),
	}, "\n")
}

func (r *runtime) acStatusText() string {
	state := r.ac.Snapshot()
	proj, err := r.store.Projection()
	contextText := "unknown / unknown"
	window, tokens := r.cfg.Model.ContextWindow, 0
	if err == nil {
		tokens = agent.EstimatedContext(proj, r.cfg)
		contextText = commaAC(tokens) + " / " + commaAC(window)
	}
	acEnabled := "disabled"
	if state.Enabled {
		acEnabled = "enabled"
	}
	piEnabled := "disabled"
	if r.settings.Compaction.Enabled {
		piEnabled = "enabled"
	}
	lines := []string{
		"pi-ac: automatic compaction " + acEnabled,
		"context: " + contextText,
		"end-of-turn compaction: " + formatACThreshold(state.EndOfTurnThreshold, window, state.EndOfTurnUsesDefault),
		"end-of-turn delay: " + formatACDelay(state.EndOfTurnDelay),
		"compactor model: " + state.CompactorModel,
	}
	if state.CompactorModel == "" {
		lines[len(lines)-1] = "compactor model: active model (no switch)"
	}
	if window > 0 {
		native := max(0, window-r.cfg.ReserveTokens)
		adaptive := "Pi threshold"
		if state.TargetPercent != nil {
			threshold := min(native, int(float64(window)**state.TargetPercent/100), ac.DefaultTargetTokens)
			adaptive = fmt.Sprintf("%s%% or %s tokens (whichever is lower; ~%s)", strconv.FormatFloat(*state.TargetPercent, 'f', -1, 64), commaAC(ac.DefaultTargetTokens), formatACTokens(threshold))
		}
		lines = append(lines,
			"adaptive target: "+adaptive,
			fmt.Sprintf("Pi threshold: ~%s · reserve %s", formatACTokens(native), formatACTokens(r.cfg.ReserveTokens)),
			fmt.Sprintf("recent growth forecast: ~%s", formatACTokens(state.Growth.EstimateNextTurnGrowth())),
		)
	}
	lines = append(lines, "Pi auto-compaction: "+piEnabled)
	if state.Compacting {
		lines = append(lines, "compaction: running")
	}
	if state.StoppedAfterError {
		lines = append(lines, "adaptive compaction stopped after an error; use /ac enable to retry")
	}
	if state.LastCompaction != nil {
		lines = append(lines, fmt.Sprintf("last compaction: %s · ~%s before", state.LastCompaction.Source, formatACTokens(state.LastCompaction.TokensBefore)))
	}
	return strings.Join(lines, "\n")
}

func formatACThreshold(threshold *ac.EndOfTurnThreshold, window int, useDefault bool) string {
	if threshold == nil {
		return "off"
	}
	if threshold.Unit == "tokens" {
		return commaAC(int(threshold.Value)) + " tokens"
	}
	if useDefault {
		effective := max(ac.DefaultEndTurnMinTokens, int(float64(window)*threshold.Value/100))
		return fmt.Sprintf("%s tokens or %s%% of context window (whichever is higher) (~%s)", formatACTokens(ac.DefaultEndTurnMinTokens), strconv.FormatFloat(threshold.Value, 'f', -1, 64), formatACTokens(effective))
	}
	result := strconv.FormatFloat(threshold.Value, 'f', -1, 64) + "% of context window"
	if window > 0 {
		result += " (~" + formatACTokens(int(float64(window)*threshold.Value/100)) + ")"
	}
	return result
}

func formatACDelay(delay time.Duration) string {
	minutes := delay.Minutes()
	if minutes == 0 {
		return "0 minutes (immediate)"
	}
	if minutes == 1 {
		return "1 minute"
	}
	return strconv.FormatFloat(minutes, 'f', -1, 64) + " minutes"
}

func formatACTokens(tokens int) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		if tokens%1_000 == 0 {
			return fmt.Sprintf("%dK", tokens/1_000)
		}
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return strconv.Itoa(tokens)
}

func commaAC(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	text := strconv.Itoa(value)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	if negative {
		return "-" + text
	}
	return text
}
