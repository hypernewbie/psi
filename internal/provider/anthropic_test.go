package provider

import (
	"encoding/json"
	"testing"
)

func TestUsageFrom(t *testing.T) {
	t.Run("full usage map with standard ints", func(t *testing.T) {
		m := map[string]any{
			"input_tokens":                100,
			"output_tokens":               50,
			"cache_read_input_tokens":     25,
			"cache_creation_input_tokens": 10,
		}
		u := usageFrom(m)
		if u.Input != 100 || u.Output != 50 || u.CacheRead != 25 || u.CacheWrite != 10 {
			t.Fatalf("usageFrom got %+v", u)
		}
		if u.TotalTokens != 185 {
			t.Fatalf("expected TotalTokens 185, got %d", u.TotalTokens)
		}
	})

	t.Run("usage map with json.Number and float", func(t *testing.T) {
		m := map[string]any{
			"input_tokens":            json.Number("40"),
			"output_tokens":           float64(20),
			"cache_read_input_tokens": int64(5),
		}
		u := usageFrom(m)
		if u.Input != 40 || u.Output != 20 || u.CacheRead != 5 {
			t.Fatalf("usageFrom json.Number/float got %+v", u)
		}
		if u.TotalTokens != 65 {
			t.Fatalf("expected TotalTokens 65, got %d", u.TotalTokens)
		}
	})

	t.Run("nil and empty map", func(t *testing.T) {
		u1 := usageFrom(nil)
		if u1 != (Usage{}) {
			t.Fatalf("usageFrom(nil) = %+v, want zero value", u1)
		}
		u2 := usageFrom(map[string]any{})
		if u2 != (Usage{}) {
			t.Fatalf("usageFrom({}) = %+v, want zero value", u2)
		}
	})
}
