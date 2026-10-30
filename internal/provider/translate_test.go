package provider

import (
	"testing"
)

func TestJsonUnmarshal(t *testing.T) {
	t.Run("valid json object", func(t *testing.T) {
		var result map[string]any
		data := []byte(`{"name":"test","count":42}`)
		if err := jsonUnmarshal(data, &result); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["name"] != "test" || result["count"].(float64) != 42 {
			t.Fatalf("unexpected unmarshal result: %+v", result)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		var result map[string]any
		data := []byte(`{invalid json`)
		if err := jsonUnmarshal(data, &result); err == nil {
			t.Fatal("expected error for malformed json, got nil")
		}
	})

	t.Run("unmarshal into slice", func(t *testing.T) {
		var result []string
		data := []byte(`["alpha","beta","gamma"]`)
		if err := jsonUnmarshal(data, &result); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 || result[0] != "alpha" {
			t.Fatalf("unexpected result: %+v", result)
		}
	})
}
