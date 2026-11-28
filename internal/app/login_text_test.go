package app

import (
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

func TestLoginCatalogCoversBothLanguages(t *testing.T) {
	for key, entry := range loginCatalog {
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

func TestLoginSelectorFollowsLanguage(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Language = config.LanguageChinese

	login := loginSelector(r)
	if !login.Chinese {
		t.Fatal("login selector is not marked Chinese")
	}
	byID := map[string]string{}
	for _, item := range login.Items {
		byID[item.ID] = item.FirstMessage
	}
	if got := byID["openai"]; !strings.Contains(got, "ChatGPT 浏览器登录") {
		t.Fatalf("openai login row = %q, want Chinese method", got)
	}
	if got := byID["openai-codex"]; !strings.Contains(got, "浏览器或设备代码登录") {
		t.Fatalf("codex login row = %q, want Chinese method", got)
	}
	rows := strings.Join(login.RenderPickerRows(80, 10, true), "\n")
	if !strings.Contains(rows, "提供方") {
		t.Fatalf("login picker header missing Chinese label:\n%s", rows)
	}

	methods := r.codexLoginMethodSelector()
	if got := methods.Items[0].Name; got != "浏览器登录（默认）" {
		t.Fatalf("browser method = %q, want Chinese", got)
	}
	if got := methods.Items[1].Name; got != "设备代码登录（无头）" {
		t.Fatalf("device method = %q, want Chinese", got)
	}

	logout := r.logoutSelector([]provider.StoredCredential{
		{ProviderID: "anthropic", Kind: provider.CredentialAPIKey},
		{ProviderID: "openai", Kind: provider.CredentialOAuth},
	}, "anthropic")
	if !logout.Chinese {
		t.Fatal("logout selector is not marked Chinese")
	}
	if got := logout.Items[0].FirstMessage; !strings.Contains(got, "API 密钥") {
		t.Fatalf("logout key row = %q, want Chinese kind", got)
	}
	if got := logout.Items[1].FirstMessage; !strings.Contains(got, "OAuth 凭据") {
		t.Fatalf("logout OAuth row = %q, want Chinese kind", got)
	}
}

func TestCodexLoginPanelFollowsLanguage(t *testing.T) {
	panel := codexLoginPanel{chinese: true, code: "ABCD-EFGH", link: "https://example.test/codex"}
	rows := strings.Join(panel.rows(80, 20, true, nil), "\n")
	for _, want := range []string{"输入代码：ABCD-EFGH", "正在等待身份验证", "登录 OpenAI Codex"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("Chinese panel missing %q:\n%s", want, rows)
		}
	}

	panel.chinese = false
	rows = strings.Join(panel.rows(80, 20, true, nil), "\n")
	for _, want := range []string{"Enter code: ABCD-EFGH", "Waiting for authentication", "Login to OpenAI Codex"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("English panel missing %q:\n%s", want, rows)
		}
	}
}
