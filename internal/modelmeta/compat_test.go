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

func TestCompatRawStringAndTypeMismatches(t *testing.T) {
	data := []byte(`{
		"str": "hello",
		"str_null": null,
		"str_wrong": 123,
		"bool_true": true,
		"bool_wrong": "notabool"
	}`)
	var c Compat
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}

	// Raw
	if raw, ok := c.Raw("str"); !ok || string(raw) != `"hello"` {
		t.Errorf("Raw(str) = %s, %t", raw, ok)
	}
	if raw, ok := c.Raw("missing"); ok || raw != nil {
		t.Errorf("Raw(missing) = %s, %t", raw, ok)
	}

	// String
	if val, hasVal, pres := c.String("str"); val != "hello" || !hasVal || !pres {
		t.Errorf("String(str) = %q, %t, %t", val, hasVal, pres)
	}
	if val, hasVal, pres := c.String("str_null"); val != "" || hasVal || !pres {
		t.Errorf("String(str_null) = %q, %t, %t", val, hasVal, pres)
	}
	if val, hasVal, pres := c.String("str_wrong"); val != "" || hasVal || !pres {
		t.Errorf("String(str_wrong) = %q, %t, %t", val, hasVal, pres)
	}
	if val, hasVal, pres := c.String("missing"); val != "" || hasVal || pres {
		t.Errorf("String(missing) = %q, %t, %t", val, hasVal, pres)
	}

	// Bool
	if val, hasVal, pres := c.Bool("bool_true"); !val || !hasVal || !pres {
		t.Errorf("Bool(bool_true) = %t, %t, %t", val, hasVal, pres)
	}
	if val, hasVal, pres := c.Bool("bool_wrong"); val || hasVal || !pres {
		t.Errorf("Bool(bool_wrong) = %t, %t, %t", val, hasVal, pres)
	}

	// IsNull
	if !c.IsNull("str_null") {
		t.Errorf("IsNull(str_null) = false, want true")
	}
	if c.IsNull("str") {
		t.Errorf("IsNull(str) = true, want false")
	}
	if c.IsNull("missing") {
		t.Errorf("IsNull(missing) = true, want false")
	}
}

