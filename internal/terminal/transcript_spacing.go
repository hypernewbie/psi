package terminal

import "unicode"

// Track only the committed row boundary, never transcript bodies or the owned
// editor/tool/thinking tail. This also covers command output and history replay.
type transcriptSpacing struct {
	filter                           Sanitizer
	haveRows, lastBlank, lineVisible bool
}

func (s *transcriptSpacing) observe(text string) {
	for _, r := range s.filter.Feed(text) {
		if r == '\n' {
			s.haveRows, s.lastBlank, s.lineVisible = true, !s.lineVisible, false
		} else if !unicode.IsSpace(r) {
			s.lineVisible = true
		}
	}
}

func (s *transcriptSpacing) gap() string {
	if s.lineVisible {
		return "\r\n\r\n"
	}
	if s.haveRows && !s.lastBlank {
		return "\r\n"
	}
	return ""
}

// EnsureTranscriptGap separates a new visible block without duplicating the
// padding already emitted by a message formatter. It never changes source text.
func (c *Console) EnsureTranscriptGap() error {
	spacing := c.transcriptSpacing
	if c.frameDepth > 0 {
		spacing.observe(c.pendingAppend.String())
	}
	if gap := spacing.gap(); gap != "" {
		return c.emitComposed(gap)
	}
	return c.err
}
