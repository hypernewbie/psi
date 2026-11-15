package skills

import "testing"

// TestParseBlock pins Pi's whole-message skill grammar: one leading block,
// the hidden body returned unchanged, and a trimmed optional argument tail.
func TestParseBlock(t *testing.T) {
	const preamble = "References are relative to /skills/doer."
	tests := []struct {
		name string
		text string
		ok   bool
		want Block
	}{
		{
			name: "valid without args",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\nBODY\n</skill>",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\nBODY"},
		},
		{
			name: "args trimmed",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\nBODY\n</skill>\n\n  run it  ",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\nBODY", Args: "run it"},
		},
		{
			name: "empty body keeps captured newlines",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\n\n</skill>",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\n"},
		},
		{
			name: "body whitespace preserved",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\n  indented\nline  \n\n</skill>",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\n  indented\nline  \n"},
		},
		{
			name: "multiline markdown",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\n- one\n- two\n\n```sh\ngo test\n```\n</skill>",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\n- one\n- two\n\n```sh\ngo test\n```"},
		},
		{
			name: "ambiguous closing tag uses the last delimiter",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\nfirst\n</skill>\nsecond\n</skill>",
			ok:   true,
			want: Block{Name: "doer", Location: "/skills/doer/SKILL.md", Body: preamble + "\n\nfirst\n</skill>\nsecond"},
		},
		{
			name: "missing location",
			text: "<skill name=\"doer\">\n" + preamble + "\n\nBODY\n</skill>",
		},
		{
			name: "missing closing tag",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\nBODY",
		},
		{
			name: "leading text",
			text: "prefix\n<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" + preamble + "\n\nBODY\n</skill>",
		},
		{
			name: "legacy psi preamble",
			text: "Skill directory: /skills/doer\nResolve relative paths from this directory.\n\nBODY",
		},
		{
			name: "carriage returns are not the Pi grammar",
			text: "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\r\n" + preamble + "\r\n\r\nBODY\r\n</skill>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBlock(tt.text)
			if ok != tt.ok {
				t.Fatalf("ParseBlock ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("ParseBlock = %+v, want %+v", got, tt.want)
			}
		})
	}
}
