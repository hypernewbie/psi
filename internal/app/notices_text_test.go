package app

import (
	"testing"

	"github.com/hypernewbie/psi/internal/config"
)

func TestNoticesCatalogCoversBothLanguages(t *testing.T) {
	for key, entry := range noticesCatalog {
		if entry.English == "" {
			t.Errorf("key %q has empty English text", key)
		}
		if entry.Chinese == "" {
			t.Errorf("key %q has empty Chinese text", key)
		}
		if entry.Chinese == entry.English {
			t.Errorf("key %q is not translated", key)
		}
	}
}

func TestLocalizedFollowsStoredLanguage(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Language = config.LanguageChinese
	if got, want := r.localized("notice.models-json-error", "boom"), "models.json 错误：boom"; got != want {
		t.Fatalf("localized notice = %q, want %q", got, want)
	}
	if got, want := r.localizedError("error.use-model").Error(), "使用 /model [provider/]model"; got != want {
		t.Fatalf("localized error = %q, want %q", got, want)
	}

	r.settings.Language = config.LanguageEnglish
	if got, want := r.localized("notice.models-json-error", "boom"), "models.json error: boom"; got != want {
		t.Fatalf("localized notice = %q, want %q", got, want)
	}
}

func TestLocalizedFallsBackToEnglish(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Language = ""
	r.options.Language = ""
	got := r.localized("notice.request-cancelled")
	if got != noticesCatalog["notice.request-cancelled"].English {
		t.Fatalf("localized notice = %q, want English default", got)
	}
}
