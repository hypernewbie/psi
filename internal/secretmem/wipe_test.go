package secretmem

import "testing"

func TestWipeClearsExactRangeOnly(t *testing.T) {
	backing := []byte("0123456789")
	rangeAlias := backing[2:5]
	Wipe(rangeAlias)
	if string(backing) != "01\x00\x00\x0056789" {
		t.Fatalf("wipe changed the wrong range: %q", backing)
	}
}

func TestWipeClearsFullCapacity(t *testing.T) {
	backing := []byte("secret")
	owned := backing[:4:6]
	Wipe(owned[:cap(owned)])
	for i, b := range backing {
		if b != 0 {
			t.Fatalf("byte %d not cleared: %q", i, backing)
		}
	}
}
