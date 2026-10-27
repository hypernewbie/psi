package terminal

// Phase G regressions for PLAN3 F09: word deletion segments once (linear)
// and preserves supported Pi word boundaries.
//
// Differential corpus against the Pi findWordBackward algorithm (compiled
// word-navigation.js from the installed package; pinned-rev parity by source
// review of temp/pi-mono word-navigation.ts). JS cursors are UTF-16 units;
// the harness converts through rune indices (BMP corpus) so byte offsets
// stay comparable.

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const piWordNavJS = "/home/hypernewbie/.nvm/versions/node/v24.11.1/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-tui/dist/word-navigation.js"

type wordCase struct {
	text   string
	cursor int // byte offset
}

func byteToRuneIdx(s string, byteOff int) int {
	n := 0
	for i := range s {
		if i >= byteOff {
			break
		}
		n++
	}
	return n
}

func runeToByteIdx(s string, runeIdx int) int {
	i := 0
	for pos := range s {
		if i == runeIdx {
			return pos
		}
		i++
	}
	return len(s)
}

func piWordBackward(t *testing.T, cases []wordCase) []int {
	t.Helper()
	if _, err := os.Stat(piWordNavJS); err != nil {
		t.Skip("pi word-navigation dist unavailable: blocked cell")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable: blocked cell")
	}
	script := `const m=require(` + "`" + piWordNavJS + "`" + `);
const rl=require('readline').createInterface({input:process.stdin});
rl.on('line',l=>{const c=JSON.parse(l);console.log(m.findWordBackward(c.text,c.cursor));});`
	cmd := exec.Command(node, "-e", script)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, c := range cases {
			// BMP corpus: UTF-16 units equal rune indices.
			if !isBMP(c.text) {
				t.Error("non-BMP case in differential corpus")
				continue
			}
			json.NewEncoder(stdin).Encode(map[string]any{"text": c.text, "cursor": byteToRuneIdx(c.text, c.cursor)})
		}
		stdin.Close()
	}()
	var out []int
	sc := bufio.NewScanner(stdout)
	for i := 0; i < len(cases); i++ {
		if !sc.Scan() {
			t.Fatalf("pi helper exited early at case %d", i)
		}
		units, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
		if err != nil {
			t.Fatalf("bad pi output %q", sc.Text())
		}
		out = append(out, runeToByteIdx(cases[i].text, units))
	}
	cmd.Wait()
	return out
}

func isBMP(s string) bool {
	for _, r := range s {
		if r > 0xFFFF {
			return false
		}
	}
	return true
}

func TestF09WordParityWithPi(t *testing.T) {
	mk := func(text string, cursor int) wordCase { return wordCase{text, cursor} }
	end := func(text string) wordCase { return wordCase{text, len(text)} }
	cases := []wordCase{
		end("hello world"),
		end("hello world   "),
		mk("hello world", 5),
		mk("hello world", 0),
		end("foo,bar;baz"),
		end("foo...bar"),
		end("a  b\tc\nd"),
		end("trailing\n\n"),
		end("hello, 世界"),
		end("世界"),
		end("école café"), // combining marks (BMP)
		end("naïve résumé"),
		end("under_score name"),
		end("a/b\\c-d_e+f=g"),
		end(strings.Repeat("ab ", 200)),
		end("word " + strings.Repeat("x", 500)),
		end("...!!!???"),
		end("mixed-word_with,punct!"),
	}
	want := piWordBackward(t, cases)
	for i, c := range cases {
		if got := wordBackward(c.text, c.cursor); got != want[i] {
			t.Errorf("case %d %q@%d: psi=%d pi=%d", i, c.text, c.cursor, got, want[i])
		}
	}
}

func TestF09LongWordScaling(t *testing.T) {
	for _, size := range []int{1024, 4096, 65536} {
		text := strings.Repeat("x", size)
		start := time.Now()
		got := wordBackward(text, len(text))
		elapsed := time.Since(start)
		if got != 0 {
			t.Fatalf("size %d: got %d", size, got)
		}
		t.Logf("long word %d bytes: %v", size, elapsed)
		if size == 65536 && elapsed > 2*time.Second {
			t.Fatalf("64KiB word took %v (quadratic regression?)", elapsed)
		}
	}
	// 256 KiB single word must complete (old code extrapolated to days).
	text256 := strings.Repeat("z", 256*1024)
	start := time.Now()
	if got := wordBackward(text256, len(text256)); got != 0 {
		t.Fatalf("256KiB: got %d", got)
	}
	t.Logf("256KiB word: %v", time.Since(start))
	// Documented divergence: ICU dictionary segments CJK ideograph runs
	// ("世界|你好"); psi treats a non-ASCII run as one word by design.
	// Pinned here so the simplification cannot silently change.
	if got := wordBackward("世界你好", len("世界你好")); got != 0 {
		t.Fatalf("CJK run behavior changed: got %d want 0", got)
	}
	// Actual-app path: Ctrl+W through the editor on a long word.
	e := NewEditor()
	e.Insert(strings.Repeat("q", 4096) + " tail")
	e.DeleteWordBackward()
	if e.Text() != strings.Repeat("q", 4096)+" " {
		t.Fatalf("editor Ctrl+W wrong: %q...", e.Text()[4090:])
	}
	_ = utf8.RuneCountInString
}
