package session

// Bounded extraction of the optional `psiWeb` display payload. The payload is
// message-envelope metadata, not model input: readers validate its size and
// shape, ignore anything invalid, and never let it change a record's outcome.

import (
	"encoding/json"
	"strings"

	"psi/internal/event"
)

const (
	// maxWebCallBytes and maxWebResultBytes bound one serialized display
	// payload. They are far above the producer caps and only reject data that
	// could not have come from this code.
	maxWebCallBytes   = 4 * 1024
	maxWebResultBytes = 8 * 1024
	maxWebCallIDBytes = 256
)

// metadataWebMeta extracts `psiWeb` from an already parsed record. The scan
// keeps the raw slice for this record only.
func metadataWebMeta(root metadataObjectView) *event.WebMeta {
	return decodeWebMeta(root["psiWeb"])
}

// decodeWebMeta decodes one bounded payload. Wrong shapes, out-of-range
// numbers, and over-limit arrays are ignored as a whole, leaving the message
// and its outcome untouched.
func decodeWebMeta(raw []byte) *event.WebMeta {
	if len(raw) == 0 || metadataNull(raw) || len(raw) > event.WebMaxMetaBytes {
		return nil
	}
	var meta event.WebMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	if meta.Result != nil {
		if meta.CallID == "" || len(meta.CallID) > maxWebCallIDBytes || len(meta.Calls) != 0 {
			return nil
		}
		if !validWebResult(meta.Result) {
			return nil
		}
		return &meta
	}
	if len(meta.Calls) == 0 || len(meta.Calls) > event.WebMaxCallsPerEntry {
		return nil
	}
	for id, call := range meta.Calls {
		if id == "" || len(id) > maxWebCallIDBytes || !validWebCall(call) {
			return nil
		}
	}
	return &meta
}

func validWebCall(call event.WebCall) bool {
	if len(call.Title) > event.WebMaxTitleBytes || len(call.Mode) > 32 {
		return false
	}
	if call.Overflow < 0 || call.Overflow > 100 {
		return false
	}
	if len(call.Entries) > event.WebMaxCallEntries {
		return false
	}
	total := len(call.Title) + len(call.Mode)
	for _, entry := range call.Entries {
		if len(entry) > event.WebMaxCallStringBytes {
			return false
		}
		total += len(entry)
	}
	return total <= maxWebCallBytes
}

func validWebResult(result *event.WebResult) bool {
	switch result.Kind {
	case "", event.WebResultPage, event.WebResultBatch, event.WebResultSearch, event.WebResultContent:
	default:
		return false
	}
	if result.OK < 0 || result.Total < 0 || result.OK > result.Total || result.Total > 100 {
		return false
	}
	if result.HasCounts && result.Total == 0 {
		// Every counted result (a batch or search) has at least one attempt.
		return false
	}
	if !result.HasCounts && (result.OK != 0 || result.Total != 0) {
		return false
	}
	if !result.HasChars && result.Chars != 0 {
		return false
	}
	if result.Sources < 0 || result.Sources > 1000 || !result.HasSources && result.Sources != 0 {
		return false
	}
	if result.HasRange {
		if result.RangeStart < 0 || result.RangeEnd < result.RangeStart {
			return false
		}
		if result.HasChars && result.RangeEnd > result.Chars {
			return false
		}
	} else if result.RangeStart != 0 || result.RangeEnd != 0 {
		return false
	}
	if len(result.Title) > 300 {
		return false
	}
	if result.Chars < 0 || result.Chars > 1<<40 {
		return false
	}
	if result.HasDuration {
		if result.DurationMs < 0 || result.DurationMs > 24*60*60*1000 {
			return false
		}
	} else if result.DurationMs != 0 {
		return false
	}
	if result.PagePreview != "" {
		units := webUTF16Units(result.PagePreview)
		return result.Kind == event.WebResultPage && result.HasChars && int64(units) <= result.Chars &&
			units <= event.WebMaxPreviewUnits && strings.Count(result.PagePreview, "\n") < event.WebMaxPreviewLines &&
			len(result.Preview) == 0 && len(result.Expanded) == 0
	}
	if len(result.Preview) > event.WebMaxPreviewLines || len(result.Expanded) > event.WebMaxPreviewLines {
		return false
	}
	units, bytes := 0, len(result.Title)
	for _, line := range result.Preview {
		lineUnits := webUTF16Units(line)
		if lineUnits > event.WebMaxPreviewUnits {
			return false
		}
		units += lineUnits
		bytes += len(line)
	}
	for _, line := range result.Expanded {
		lineUnits := webUTF16Units(line)
		if lineUnits > event.WebMaxPreviewUnits {
			return false
		}
		units += lineUnits
		bytes += len(line)
	}
	return units <= 2*event.WebMaxPreviewUnits && bytes <= maxWebResultBytes
}

// webUTF16Units counts UTF-16 code units, matching Pi's display convention.
func webUTF16Units(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return units
}
