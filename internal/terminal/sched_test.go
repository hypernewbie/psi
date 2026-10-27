package terminal

import "testing"

func TestWriteBounded64KiB(t *testing.T) {
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'a'
	}
	batches := Batches(string(big), 64*1024)
	if len(batches) < 3 {
		t.Fatalf("expected bounded batches, got %d", len(batches))
	}
	for _, b := range batches {
		if len(b) > 64*1024 {
			t.Fatalf("batch exceeds 64KiB: %d", len(b))
		}
	}
}
