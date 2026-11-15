package config

// Prompt languages supported by the built-in prompt.
const (
	LanguageEnglish = "en"
	LanguageChinese = "zh"
)

// ValidLanguage reports whether language selects a supported built-in prompt.
func ValidLanguage(language string) bool {
	return language == LanguageEnglish || language == LanguageChinese
}
