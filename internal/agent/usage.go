package agent

import (
	"psi/internal/provider"
	"psi/internal/session"
)

func sessionUsage(usage provider.Usage) session.Usage {
	result := session.Usage{Input: float64(usage.Input), Output: float64(usage.Output), CacheRead: float64(usage.CacheRead), CacheWrite: float64(usage.CacheWrite), TotalTokens: float64(usage.TotalTokens)}
	if usage.CacheWrite1h != nil {
		n := float64(*usage.CacheWrite1h)
		result.CacheWrite1h = &n
	}
	if usage.Reasoning != nil {
		n := float64(*usage.Reasoning)
		result.Reasoning = &n
	}
	return result
}
