package main

import "time"

// resetPolicy is the effective automatic session lifetime.
type resetPolicy struct {
	mode        string
	atHour      int
	idleMinutes int
}

// resolveResetPolicy follows the existing precedence: an explicit override,
// then the per-type entry, then the base entry, then the daily default.
func resolveResetPolicy(session *sessionSettings, resetType string, override *resetSettings) resetPolicy {
	var base *resetSettings
	var byType *resetSettings
	if session != nil {
		base = session.Reset
		if override == nil && session.ByType != nil {
			switch resetType {
			case "direct":
				byType = session.ByType.Direct
			case "group":
				byType = session.ByType.Group
			case "thread":
				byType = session.ByType.Thread
			}
		}
	}
	if override != nil {
		base = override
	}
	mode := ""
	switch {
	case byType != nil && byType.Mode != "":
		mode = byType.Mode
	case byType != nil && base != nil && base.Mode != "":
		// A per-type entry without its own mode inherits the base mode.
		mode = base.Mode
	case byType != nil:
		mode = "daily"
	case base != nil && base.Mode != "":
		mode = base.Mode
	case base != nil:
		mode = "daily"
	default:
		mode = "none"
	}
	atHour := defaultAtHour
	if base != nil && base.AtHour != nil {
		atHour = *base.AtHour
	}
	if byType != nil && byType.AtHour != nil {
		atHour = *byType.AtHour
	}
	if atHour < 0 {
		atHour = 0
	}
	if atHour > 23 {
		atHour = 23
	}
	var idle *int
	if base != nil {
		idle = base.IdleMinutes
	}
	if byType != nil && byType.IdleMinutes != nil {
		idle = byType.IdleMinutes
	}
	idleMinutes := 0
	if idle != nil {
		idleMinutes = max(*idle, 0)
	}
	return resetPolicy{mode: mode, atHour: atHour, idleMinutes: idleMinutes}
}

// dailyResetAt returns the most recent boundary at the configured local hour.
func dailyResetAt(now time.Time, atHour int) time.Time {
	year, month, day := now.Date()
	today := time.Date(year, month, day, atHour, 0, 0, 0, now.Location())
	if now.Before(today) {
		// Rebuild yesterday from now so a daylight-saving shift cannot carry
		// today's offset into the prior day.
		yesterday := now.AddDate(0, 0, -1)
		year, month, day = yesterday.Date()
		return time.Date(year, month, day, atHour, 0, 0, 0, now.Location())
	}
	return today
}

// sessionFresh reports whether a stored conversation is still current.
// Daily reset compares the session start, while idle reset compares the last
// interaction, as the existing behavior does.
func sessionFresh(startedAt, lastInteraction, now time.Time, policy resetPolicy) (bool, string) {
	if policy.mode == "none" {
		return true, ""
	}
	if startedAt.IsZero() {
		startedAt = now
	}
	if lastInteraction.IsZero() {
		lastInteraction = startedAt
	}
	staleDaily := false
	if policy.mode == "daily" {
		staleDaily = startedAt.Before(dailyResetAt(now, policy.atHour))
	}
	staleIdle := false
	if policy.idleMinutes > 0 {
		staleIdle = now.After(lastInteraction.Add(time.Duration(policy.idleMinutes) * time.Minute))
	}
	switch {
	case staleDaily && staleIdle:
		daily := dailyResetAt(now, policy.atHour)
		idle := lastInteraction.Add(time.Duration(policy.idleMinutes) * time.Minute)
		if !daily.After(idle) {
			return false, "daily"
		}
		return false, "idle"
	case staleIdle:
		return false, "idle"
	case staleDaily:
		return false, "daily"
	default:
		return true, ""
	}
}
