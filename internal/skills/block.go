package skills

import (
	"regexp"
	"strings"
)

// Block is Pi's parsed skill invocation: the leading block and the optional
// argument tail.
type Block struct {
	Name     string
	Location string
	Body     string
	Args     string
}

// blockPattern is Pi's skill block grammar. Only one leading block matches;
// the argument tail is captured separately and trimmed by ParseBlock.
var blockPattern = regexp.MustCompile(`(?s)^<skill name="([^"]+)" location="([^"]+)">\n(.*?)\n</skill>(?:\n\n(.+))?$`)

// ParseBlock recognizes one complete Pi skill invocation. It is pure: the
// captured body is returned unchanged, and the stored location is data only.
func ParseBlock(text string) (Block, bool) {
	match := blockPattern.FindStringSubmatch(text)
	if match == nil {
		return Block{}, false
	}
	return Block{Name: match[1], Location: match[2], Body: match[3], Args: strings.TrimSpace(match[4])}, true
}
