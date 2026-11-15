package config

import "testing"

func TestEscapeCredentialLiteralRoundTrips(t *testing.T) {
	cases := []string{
		"plain", "$", "$$", "!", "!$", "$!", "${VAR}", "a$b!c", "!sk-test-$-!",
	}
	lookup := func(string) (string, bool) { return "env-value", true }
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got, err := ResolveCredentialWithEnv(EscapeCredentialLiteral(raw), lookup)
			if err != nil {
				t.Fatalf("ResolveCredentialWithEnv: %v", err)
			}
			if got != raw {
				t.Fatalf("round trip = %q, want %q", got, raw)
			}
		})
	}
}
