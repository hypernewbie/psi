// Package i18n selects localized text from stable message keys.
//
// Catalogs keep English and Simplified Chinese side by side so a new surface
// must supply both translations in the same change. Keys are identifiers;
// they are never translated or derived from display text.
package i18n

// Key identifies one message. The same key is used in every language.
type Key string

// Entry is one message in both supported languages.
type Entry struct {
	English string
	Chinese string
}

// Catalog maps stable keys to both language variants for one surface.
type Catalog map[Key]Entry

// Text returns the Chinese text when chinese is true and a Chinese entry
// exists. Otherwise it returns English. A missing key returns the key itself
// so an unlocalized row is visible in review instead of rendering empty.
func (c Catalog) Text(chinese bool, key Key) string {
	entry, ok := c[key]
	if !ok {
		return string(key)
	}
	if chinese && entry.Chinese != "" {
		return entry.Chinese
	}
	return entry.English
}
