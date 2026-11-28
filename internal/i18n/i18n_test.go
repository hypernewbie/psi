package i18n

import "testing"

func TestCatalogText(t *testing.T) {
	catalog := Catalog{
		"both":         {English: "English text", Chinese: "中文文本"},
		"english-only": {English: "Only English"},
	}
	for _, tt := range []struct {
		name    string
		chinese bool
		key     Key
		want    string
	}{
		{name: "Chinese entry", chinese: true, key: "both", want: "中文文本"},
		{name: "English entry", chinese: false, key: "both", want: "English text"},
		{name: "missing Chinese falls back", chinese: true, key: "english-only", want: "Only English"},
		{name: "missing key returns the key", chinese: true, key: "absent", want: "absent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalog.Text(tt.chinese, tt.key); got != tt.want {
				t.Fatalf("Text(%t, %q) = %q, want %q", tt.chinese, tt.key, got, tt.want)
			}
		})
	}
}
