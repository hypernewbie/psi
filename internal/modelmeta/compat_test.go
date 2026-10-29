package modelmeta

import (
	"encoding/json"
	"testing"
)

func TestCompatPreservesFalseNullAndAbsent(t *testing.T) {
	var got Compat
	if err := json.Unmarshal([]byte(`{"enabled":false,"cleared":null,"unknown":"v"}`), &got); err != nil {
		t.Fatal(err)
	}
	if value, hasValue, present := got.Bool("enabled"); value || !hasValue || !present {
		t.Fatalf("false = %t,%t,%t", value, hasValue, present)
	}
	if value, hasValue, present := got.Bool("cleared"); value || hasValue || !present || !got.IsNull("cleared") {
		t.Fatalf("null = %t,%t,%t null=%t", value, hasValue, present, got.IsNull("cleared"))
	}
	if value, hasValue, present := got.Bool("absent"); value || hasValue || present {
		t.Fatalf("absent = %t,%t,%t", value, hasValue, present)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if string(roundTrip["enabled"]) != "false" || string(roundTrip["cleared"]) != "null" || string(roundTrip["unknown"]) != `"v"` {
		t.Fatalf("compat fields changed: %s", encoded)
	}
}
