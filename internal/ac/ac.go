// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Hypernewbie
//
// Native Go port of the policy in hypernewbie/pi-ac, based on local source
// revision 96e92f5fcfe046466366bc5bc2972b813c03b09a.
// See LICENSE in this directory.
package ac

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTargetPercent        = 50.0
	DefaultTargetTokens         = 230_000
	DefaultEndTurnPercent       = 40.0
	DefaultEndTurnMinTokens     = 200_000
	DefaultEndTurnDelay         = 10 * time.Minute
	maxGrowthSamples            = 5
	maxEndTurnDelayMilliseconds = 2_147_483_647
)

type EndOfTurnThreshold struct {
	Unit  string // "tokens" or "percent"
	Value float64
}

type LastCompaction struct {
	Source       string
	TokensBefore int
}

type GrowthHistory struct {
	PreviousTokens *int
	Deltas         []int
}

func (h *GrowthHistory) Reset() {
	h.PreviousTokens = nil
	h.Deltas = nil
}

func (h *GrowthHistory) Observe(tokens int) {
	if h.PreviousTokens != nil && tokens > *h.PreviousTokens {
		h.Deltas = append(h.Deltas, tokens-*h.PreviousTokens)
		if len(h.Deltas) > maxGrowthSamples {
			h.Deltas = append([]int(nil), h.Deltas[len(h.Deltas)-maxGrowthSamples:]...)
		}
	}
	previous := tokens
	h.PreviousTokens = &previous
}

// EstimateNextTurnGrowth returns the conservative p90 from the last five
// positive context increases. With five or fewer samples this is their maximum.
func (h GrowthHistory) EstimateNextTurnGrowth() int {
	if len(h.Deltas) == 0 {
		return 0
	}
	sorted := append([]int(nil), h.Deltas...)
	sort.Ints(sorted)
	index := int(math.Ceil(float64(len(sorted))*0.9)) - 1
	return sorted[max(0, min(index, len(sorted)-1))]
}

func ParseEndOfTurnThreshold(value string) (*EndOfTurnThreshold, bool) {
	input := strings.ToLower(strings.TrimSpace(value))
	if input == "off" {
		return nil, true
	}
	if input == "" {
		return nil, false
	}
	if allDigits(input) {
		n, err := strconv.ParseInt(input, 10, 64)
		if err == nil && n > 0 && n <= 9_007_199_254_740_991 {
			return &EndOfTurnThreshold{Unit: "tokens", Value: float64(n)}, true
		}
		return nil, false
	}
	if !strings.HasSuffix(input, "%") {
		return nil, false
	}
	percentText := strings.TrimSuffix(input, "%")
	if !percentageNumber(percentText) {
		return nil, false
	}
	n, err := strconv.ParseFloat(percentText, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 100 {
		return nil, false
	}
	return &EndOfTurnThreshold{Unit: "percent", Value: n}, true
}

func ShouldCompactAtEndOfTurn(tokens, contextWindow int, threshold *EndOfTurnThreshold, minTokens int) bool {
	if threshold == nil || tokens < 0 || contextWindow <= 0 {
		return false
	}
	var limit int
	switch threshold.Unit {
	case "tokens":
		if threshold.Value <= 0 || threshold.Value > float64(math.MaxInt) || math.Trunc(threshold.Value) != threshold.Value {
			return false
		}
		limit = int(threshold.Value)
	case "percent":
		if threshold.Value <= 0 || threshold.Value > 100 || math.IsNaN(threshold.Value) || math.IsInf(threshold.Value, 0) {
			return false
		}
		limit = int(math.Floor(float64(contextWindow) * threshold.Value / 100))
	default:
		return false
	}
	if minTokens > 0 {
		limit = max(limit, minTokens)
	}
	return tokens >= limit
}

// ShouldAutoCompact applies pi-ac's predictive threshold while leaving an
// already-crossed native threshold to psi's existing compaction path.
func ShouldAutoCompact(tokens, contextWindow, reserveTokens int, nativeEnabled bool, targetPercent *float64, predictedGrowth int) bool {
	if !nativeEnabled || tokens < 0 || contextWindow <= 0 || reserveTokens < 0 {
		return false
	}
	if targetPercent != nil && (*targetPercent <= 0 || *targetPercent > 100 || math.IsNaN(*targetPercent) || math.IsInf(*targetPercent, 0)) {
		return false
	}
	nativeThreshold := max(0, contextWindow-reserveTokens)
	if tokens >= nativeThreshold {
		return false
	}
	policyThreshold := nativeThreshold
	if targetPercent != nil {
		policyThreshold = min(policyThreshold, int(math.Floor(float64(contextWindow)**targetPercent/100)))
		policyThreshold = min(policyThreshold, DefaultTargetTokens)
	}
	return tokens >= policyThreshold || max(0, predictedGrowth) >= policyThreshold-tokens
}

// Settings is session-local. None of these controls are persisted to settings.json.
type Settings struct {
	Enabled              bool
	TargetPercent        *float64
	EndOfTurnThreshold   *EndOfTurnThreshold
	EndOfTurnUsesDefault bool
	EndOfTurnDelay       time.Duration
	CompactorModel       string
	StoppedAfterError    bool
	Compacting           bool
	Growth               GrowthHistory
	LastCompaction       *LastCompaction
	CompactionCount      uint64
}

type Controller struct {
	mu       sync.Mutex
	settings Settings
}

func NewController() *Controller {
	percent := DefaultTargetPercent
	threshold := &EndOfTurnThreshold{Unit: "percent", Value: DefaultEndTurnPercent}
	return &Controller{settings: Settings{
		Enabled:              true,
		TargetPercent:        &percent,
		EndOfTurnThreshold:   threshold,
		EndOfTurnUsesDefault: true,
		EndOfTurnDelay:       DefaultEndTurnDelay,
	}}
}

func (c *Controller) Snapshot() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneSettings(c.settings)
}

func cloneSettings(s Settings) Settings {
	if s.TargetPercent != nil {
		value := *s.TargetPercent
		s.TargetPercent = &value
	}
	if s.EndOfTurnThreshold != nil {
		value := *s.EndOfTurnThreshold
		s.EndOfTurnThreshold = &value
	}
	if s.LastCompaction != nil {
		value := *s.LastCompaction
		s.LastCompaction = &value
	}
	s.Growth.Deltas = append([]int(nil), s.Growth.Deltas...)
	if s.Growth.PreviousTokens != nil {
		value := *s.Growth.PreviousTokens
		s.Growth.PreviousTokens = &value
	}
	return s
}

func (c *Controller) ResetSession() {
	fresh := NewController()
	c.mu.Lock()
	c.settings = fresh.settings
	c.mu.Unlock()
}

func (c *Controller) ResetGrowth() {
	c.mu.Lock()
	c.settings.Growth.Reset()
	c.mu.Unlock()
}

// ObserveTurn stores completed-turn growth and returns whether proactive
// compaction should run before a continuation. Pending messages suppress it.
func (c *Controller) ObserveTurn(tokens, contextWindow, reserveTokens int, nativeEnabled, canContinue, hasPendingMessages bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Growth.Observe(tokens)
	if !canContinue || hasPendingMessages || !c.settings.Enabled || c.settings.StoppedAfterError {
		return false
	}
	return ShouldAutoCompact(tokens, contextWindow, reserveTokens, nativeEnabled, c.settings.TargetPercent, c.settings.Growth.EstimateNextTurnGrowth())
}

func (c *Controller) ShouldCompactAtEndOfTurn(tokens, contextWindow int, nativeEnabled, idle, hasPendingMessages bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return idle && !hasPendingMessages && c.settings.Enabled && !c.settings.StoppedAfterError && nativeEnabled &&
		ShouldCompactAtEndOfTurn(tokens, contextWindow, c.settings.EndOfTurnThreshold, endTurnMinimum(c.settings.EndOfTurnUsesDefault))
}

func endTurnMinimum(useDefault bool) int {
	if useDefault {
		return DefaultEndTurnMinTokens
	}
	return 0
}

func (c *Controller) RecordCompaction(success bool, source string, tokensBefore int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Compacting = false
	c.settings.Growth.Reset()
	if !success {
		c.settings.StoppedAfterError = true
		return
	}
	c.settings.LastCompaction = &LastCompaction{Source: source, TokensBefore: tokensBefore}
	c.settings.CompactionCount++
}

func (c *Controller) SetEnabled(enabled bool) {
	c.mu.Lock()
	c.settings.Enabled = enabled
	if enabled {
		c.settings.StoppedAfterError = false
		c.settings.Growth.Reset()
	}
	c.mu.Unlock()
}

func (c *Controller) SetTargetPercent(value *float64) {
	c.mu.Lock()
	if value == nil {
		c.settings.TargetPercent = nil
	} else {
		copy := *value
		c.settings.TargetPercent = &copy
	}
	c.mu.Unlock()
}

func (c *Controller) SetEndOfTurnThreshold(value *EndOfTurnThreshold, useDefault bool) {
	c.mu.Lock()
	if value == nil {
		c.settings.EndOfTurnThreshold = nil
	} else {
		copy := *value
		c.settings.EndOfTurnThreshold = &copy
	}
	c.settings.EndOfTurnUsesDefault = useDefault
	c.mu.Unlock()
}

func (c *Controller) SetEndOfTurnDelay(delay time.Duration) {
	c.mu.Lock()
	c.settings.EndOfTurnDelay = delay
	c.mu.Unlock()
}

func (c *Controller) SetCompacting(compacting bool) {
	c.mu.Lock()
	c.settings.Compacting = compacting
	c.mu.Unlock()
}

func (c *Controller) SetCompactorModel(model string) {
	c.mu.Lock()
	c.settings.CompactorModel = model
	c.mu.Unlock()
}

func ParseEndOfTurnDelay(value string) (time.Duration, bool) {
	input := strings.TrimSpace(value)
	if !decimalNumber(input) {
		return 0, false
	}
	minutes, err := strconv.ParseFloat(input, 64)
	if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 0 || minutes*60_000 > maxEndTurnDelayMilliseconds {
		return 0, false
	}
	return time.Duration(minutes * float64(time.Minute)), true
}

func percentageNumber(value string) bool {
	if value == "" {
		return false
	}
	dot := false
	digitsBefore, digitsAfter := 0, 0
	for _, ch := range value {
		if ch == '.' {
			if dot || digitsBefore == 0 {
				return false
			}
			dot = true
			continue
		}
		if ch < '0' || ch > '9' {
			return false
		}
		if dot {
			digitsAfter++
		} else {
			digitsBefore++
		}
	}
	return digitsBefore > 0 && (!dot || digitsAfter > 0)
}

func decimalNumber(value string) bool {
	if value == "" {
		return false
	}
	dots, digits := 0, 0
	for _, ch := range value {
		if ch == '.' {
			dots++
			if dots > 1 {
				return false
			}
			continue
		}
		if ch < '0' || ch > '9' {
			return false
		}
		digits++
	}
	return digits > 0
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
