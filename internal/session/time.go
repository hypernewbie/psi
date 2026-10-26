package session

import (
	"time"
)

// parseTimeMillis parses Pi ISO-8601 entry timestamps to Unix milliseconds.
// Falls back to 0 on parse failure (legacy tolerance); callers keep the
// original string in the session file.
func parseTimeMillis(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli()
	}
	// Pi writes ISO strings via Date.toISOString (RFC3339Nano compatible).
	// Try a final lenient parse.
	if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", s); err == nil {
		return t.UnixMilli()
	}
	return 0
}

func millisToISO(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}
