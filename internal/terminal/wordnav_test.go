package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"psi/internal/testutil"
)

// The adapter built by scripts/build-reference.mjs imports unmodified source
// from the pinned Pi checkout. No private installation path or inherited
// Node environment participates in this optional reference test.
var testPiWordModule = flag.String("psi-test-pi-word-module", "", "pinned Pi reference adapter URL for word-navigation tests")

type wordCase struct {
	text   string
	cursor int // byte offset
}

func piWordBackward(t *testing.T, cases []wordCase) []int {
	t.Helper()
	if *testPiWordModule == "" {
		t.Skip("Pi word-navigation reference cell blocked: supply -psi-test-pi-word-module")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the explicitly requested reference test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := `import {readFileSync} from 'node:fs';
const m=await import(process.argv[1]);
const cases=JSON.parse(readFileSync(0,'utf8'));
console.log(JSON.stringify(await Promise.all(cases.map(c=>m.findWordBackward(c.text,c.cursor)))));`
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, *testPiWordModule)
	cmd.Env = testutil.ChildEnv(t.TempDir(), map[string]string{"LANG": "C.UTF-8"})
	var input []map[string]any
	for _, c := range cases {
		input = append(input, map[string]any{"text": c.text, "cursor": len(utf16.Encode([]rune(c.text[:c.cursor])))})
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pinned word-navigation reference failed: %v", err)
	}
	var units []int
	if err := json.Unmarshal(out, &units); err != nil || len(units) != len(cases) {
		t.Fatalf("invalid reference output: %v", err)
	}
	var offsets []int
	for i, target := range units {
		n, offset := 0, len(cases[i].text)
		for pos, r := range cases[i].text {
			if n == target {
				offset = pos
				break
			}
			n += utf16.RuneLen(r)
		}
		offsets = append(offsets, offset)
	}
	return offsets
}

func TestF09WordParityWithPi(t *testing.T) {
	end := func(text string) wordCase { return wordCase{text, len(text)} }
	cases := []wordCase{
		end("hello world"), end("hello world   "), {"hello world", 5}, {"hello world", 0},
		end("foo,bar;baz"), end("foo...bar"), end("a  b\tc\nd"), end("trailing\n\n"),
		end("hello, 世界"), end("世界"), end("école café"), end("naïve résumé"),
		end("under_score name"), end("under_score"), end("a/b\\c-d_e+f=g"),
		end(strings.Repeat("ab ", 200)), end("word " + strings.Repeat("x", 500)),
		end("...!!!???"), end("mixed-word_with,punct!"),
		end("a🙂"), end("a👩‍🚀"), end("hello 🇳🇿"), end("word™"), end("a🛰️"),
	}
	want := piWordBackward(t, cases)
	for i, c := range cases {
		if got := wordBackward(c.text, c.cursor); got != want[i] {
			t.Errorf("case %d %q@%d: psi=%d pi=%d", i, c.text, c.cursor, got, want[i])
		}
	}
}

func TestWordBackwardSymbolsKeepPrecedingWord(t *testing.T) {
	for text, prefix := range map[string]string{"a🙂": "a", "a👩‍🚀": "a", "word™": "word", "a🛰️": "a"} {
		e := NewEditor()
		e.Insert(text)
		e.DeleteWordBackward()
		if e.Text() != prefix {
			t.Errorf("Ctrl+W on %q: got %q want %q", text, e.Text(), prefix)
		}
	}
}

func TestF09LongWordScaling(t *testing.T) {
	for _, size := range []int{1024, 4096, 65536, 256 * 1024} {
		text := strings.Repeat("x", size)
		start := time.Now()
		got := wordBackward(text, len(text))
		elapsed := time.Since(start)
		if got != 0 {
			t.Fatalf("size %d: got %d", size, got)
		}
		t.Logf("long word %d bytes: %v", size, elapsed)
		// Deadlock guard only. Latency acceptance needs actual-app samples.
		if elapsed > 2*time.Second {
			t.Fatalf("long-word navigation stalled: %v", elapsed)
		}
	}
	// Documented dictionary limitation, separate from the supported corpus.
	if got := wordBackward("世界你好", len("世界你好")); got != 0 {
		t.Fatalf("CJK run behavior changed: got %d want 0", got)
	}
	e := NewEditor()
	e.Insert(strings.Repeat("q", 4096) + " tail")
	e.DeleteWordBackward()
	if e.Text() != strings.Repeat("q", 4096)+" " {
		t.Fatal("editor Ctrl+W deleted the wrong prefix")
	}
}
