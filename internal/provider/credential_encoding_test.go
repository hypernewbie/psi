package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
)

func TestEncodeCredentialLiteralPiAndJSONEscaping(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "leading bang and dollar", raw: []byte("!sk-test-$-!"), want: `"$!sk-test-$$-!"`},
		{name: "non-leading bang", raw: []byte("a$!b"), want: `"a$$!b"`},
		{name: "quote and backslash", raw: []byte("a\"b\\c"), want: `"a\"b\\c"`},
		{name: "controls", raw: []byte{0x01, '\b', '\t'}, want: `"\u0001\b\t"`},
		{name: "unicode preserved", raw: []byte("密钥"), want: `"密钥"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeCredentialLiteral(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("encoded = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEncodeCredentialLiteralRoundTripsThroughPiResolution(t *testing.T) {
	cases := [][]byte{
		[]byte("plain"), []byte("$"), []byte("$$"), []byte("!"), []byte("!$"),
		[]byte("$!"), []byte("${VAR}"), []byte("a$b!c"), []byte("!sk-test-$-!"),
	}
	lookup := func(string) (string, bool) { return "env-value", true }
	for _, raw := range cases {
		encoded, err := encodeCredentialLiteral(raw)
		if err != nil {
			t.Fatal(err)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("JSON decode %s: %v", encoded, err)
		}
		got, err := config.ResolveCredentialWithEnv(decoded, lookup)
		if err != nil {
			t.Fatalf("ResolveCredentialWithEnv(%q): %v", decoded, err)
		}
		if got != string(raw) {
			t.Fatalf("round trip = %q, want %q (encoded %s)", got, raw, encoded)
		}
	}
}

func TestEncodeCredentialLiteralRejectsOversizeBeforeAllocation(t *testing.T) {
	raw := bytes.Repeat([]byte("$"), maxAuthJSONSize)
	if _, err := encodeCredentialLiteral(raw); !errors.Is(err, errCredentialTooLarge) {
		t.Fatalf("oversize credential error = %v", err)
	}
}

func TestEncodeCredentialLiteralRejectsInvalidUTF8(t *testing.T) {
	if _, err := encodeCredentialLiteral([]byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}
