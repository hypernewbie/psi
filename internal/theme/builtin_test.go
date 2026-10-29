package theme

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestEmbeddedPiBuiltinThemesRemainUnmodifiedAndResolve(t *testing.T) {
	wantHashes := map[string]string{
		"dark":  "e1f96982f8cb7d2a48eca7db03c9cb282cb1e14a243680cd855638ecd6a251b6",
		"light": "2dee6a4be7f765e7fd7728166b7ebf8c29e4ce313f8ec6b38ae50699e3abc2f3",
	}
	wantColors := map[string]map[string]string{
		"dark":  {"accent": "#a798d7", "border": "#5fa8cc", "userMessageBg": "#213b49", "toolPendingBg": "#34383a", "mdHeading": "#cd9a22", "thinkingMax": "#fe5462"},
		"light": {"accent": "#7459b4", "border": "#3d8eb3", "userMessageBg": "#dfe7ec", "toolPendingBg": "#e4e5e6", "mdHeading": "#8f6802", "thinkingMax": "#fe7479"},
	}
	for name, wantHash := range wantHashes {
		t.Run(name, func(t *testing.T) {
			data, err := BuiltinJSON(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantHash {
				t.Fatalf("Pi source hash=%s, want %s", got, wantHash)
			}
			doc, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := doc.Compile(ColorModeTruecolor)
			if err != nil {
				t.Fatal(err)
			}
			if len(resolved.Colors()) != 56 {
				t.Fatalf("resolved roles=%d", len(resolved.Colors()))
			}
			for token, want := range wantColors[name] {
				color, ok := resolved.Colors()[token]
				if !ok {
					t.Fatalf("missing token %s", token)
				}
				got, err := ColorToHex(color.Color)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s=%s, want Pi %s", token, got, want)
				}
			}
		})
	}
}
