package terminal

import "testing"

func TestBangShellParse(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    ShellCommand
		matched bool
	}{
		{"included", "!printf '%s\\n' hi", ShellCommand{Command: "printf '%s\\n' hi"}, true},
		{"excluded", "!!secret command", ShellCommand{Command: "secret command", Exclude: true}, true},
		{"trim unicode", "\u00a0\u2003 ! echo hi \n", ShellCommand{Command: "echo hi"}, true},
		{"multiline quotes", "!printf '%s\\n' 'a\nb'", ShellCommand{Command: "printf '%s\\n' 'a\nb'"}, true},
		{"empty", "!", ShellCommand{}, false},
		{"empty excluded", "!!", ShellCommand{}, false},
		{"whitespace only", "  !!  \t", ShellCommand{}, false},
		{"triple bang", "!!! ls", ShellCommand{Command: "! ls", Exclude: true}, true},
		{"ordinary", "echo hi", ShellCommand{}, false},
		{"slash command", "/quit", ShellCommand{}, false},
		{"bang in middle", "echo ! hi", ShellCommand{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := ParseShellCommand(tc.input)
			if matched != tc.matched || got != tc.want {
				t.Fatalf("ParseShellCommand(%q) = %+v, %v; want %+v, %v", tc.input, got, matched, tc.want, tc.matched)
			}
		})
	}
}
