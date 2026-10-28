package terminal

import (
	"os"
	"strings"
	"testing"
)

func TestFormatNoticeUsesSemanticStylesAndSafeText(t *testing.T) {
	colored := strings.Join(FormatNotice(NoticeError, "write failed\x1b]0;hidden\a with details", 24, false), "\n")
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		if strings.Contains(colored, "\x1b[") {
			t.Fatalf("NO_COLOR error notice emitted SGR: %q", colored)
		}
	} else if !strings.Contains(colored, "\x1b[31mError: ") {
		t.Fatalf("error notice lacks error style: %q", colored)
	}
	if strings.Contains(colored, "hidden") || strings.Contains(colored, "\x1b]0") {
		t.Fatalf("error notice leaked terminal control payload: %q", colored)
	}
	plain := strings.Join(FormatNotice(NoticeWarning, "offline and readable", 40, true), "\n")
	if strings.Contains(plain, "\x1b[") || !strings.Contains(plain, "Warning: offline and readable") {
		t.Fatalf("NO_COLOR warning notice mismatch: %q", plain)
	}
	success := strings.Join(FormatNotice(NoticeSuccess, "saved", 40, false), "\n")
	if !strings.Contains(success, "\x1b[32mSuccess: saved") {
		t.Fatalf("success notice style mismatch: %q", success)
	}
}

func TestFormatNoticeWrapsUntrustedMultilineDetails(t *testing.T) {
	rows := FormatNotice(NoticeError, "first line\nsecond line with enough content to wrap safely", 24, true)
	if len(rows) < 3 {
		t.Fatalf("notice was not wrapped: %q", rows)
	}
	for _, row := range rows {
		if CellWidth(row) > 23 {
			t.Fatalf("notice row exceeds terminal width: %q (%d)", row, CellWidth(row))
		}
	}
	if joined := strings.Join(rows, "\n"); !strings.Contains(joined, "first line") || !strings.Contains(joined, "second line") {
		t.Fatalf("notice details were lost: %q", joined)
	}
}
