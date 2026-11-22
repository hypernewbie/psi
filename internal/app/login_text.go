package app

import (
	"fmt"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/i18n"
)

// loginCatalog holds the app-authored sign-in, sign-out, and credential text.
// It shares the stable-key and both-languages rules with noticesCatalog.
var loginCatalog = i18n.Catalog{
	"login.method.chatgpt": {
		English: "ChatGPT browser sign-in",
		Chinese: "ChatGPT 浏览器登录",
	},
	"login.method.codex": {
		English: "browser or device code sign-in",
		Chinese: "浏览器或设备代码登录",
	},
	"login.method.browser": {
		English: "Browser login (default)",
		Chinese: "浏览器登录（默认）",
	},
	"login.method.device": {
		English: "Device code login (headless)",
		Chinese: "设备代码登录（无头）",
	},
	"login.kind.api-key": {
		English: "API key",
		Chinese: "API 密钥",
	},
	"login.kind.oauth": {
		English: "OAuth credential",
		Chinese: "OAuth 凭据",
	},
	"login.providers-label": {
		English: "Providers",
		Chinese: "提供方",
	},
	"login.no-providers": {
		English: "No matching providers",
		Chinese: "没有匹配的提供方",
	},
	"login.credentials-label": {
		English: "Stored credentials",
		Chinese: "已存凭据",
	},
	"login.no-credentials": {
		English: "No matching credentials",
		Chinese: "没有匹配的凭据",
	},
	"login.codex-method-label": {
		English: "Select OpenAI Codex login method:",
		Chinese: "选择 OpenAI Codex 登录方式：",
	},
	"login.picker-matches": {
		English: "%s · %d matches",
		Chinese: "%s · %d 个匹配",
	},
	"login.type-to-search": {
		English: "Type to search · Esc/Ctrl+C cancel",
		Chinese: "输入以搜索 · Esc/Ctrl+C 取消",
	},
	"login.picker-hint": {
		English: "↑/↓ select · PageUp/PageDown move · Enter apply · Esc cancel",
		Chinese: "↑/↓ 选择 · PageUp/PageDown 移动 · Enter 应用 · Esc 取消",
	},
	"login.picker-hint-short": {
		English: "↑↓ select · Enter apply · Esc cancel",
		Chinese: "↑↓ 选择 · Enter 应用 · Esc 取消",
	},
	"login.codex-method-hint": {
		English: "↑↓ navigate · Enter select · Esc cancel",
		Chinese: "↑↓ 选择 · Enter 确认 · Esc 取消",
	},
	"login.api-key-status": {
		English: "API key for %s · Enter save · Esc cancel",
		Chinese: "%s 的 API 密钥 · Enter 保存 · Esc 取消",
	},
	"login.requires-terminal": {
		English: "%s requires an interactive terminal",
		Chinese: "%s 需要交互式终端",
	},
	"login.requires-session": {
		English: "%s requires an active session",
		Chinese: "%s 需要活动会话",
	},
	"login.storage-blocked": {
		English: "storage is blocked: ",
		Chinese: "存储被阻塞：",
	},
	"login.unavailable-busy": {
		English: "%s is unavailable while a run or picker is active",
		Chinese: "运行或选择器活动时 %s 不可用",
	},
	"login.use-login": {
		English: "use /login [provider]",
		Chinese: "使用 /login [provider]",
	},
	"login.anthropic-unavailable": {
		English: "Anthropic login is not available; set ANTHROPIC_API_KEY or add an api_key entry to %s.",
		Chinese: "Anthropic 登录不可用；请设置 ANTHROPIC_API_KEY，或向 %s 添加 api_key 条目。",
	},
	"login.unknown-provider": {
		English: "unknown provider %q; supported: %s",
		Chinese: "未知提供方 %q；支持：%s",
	},
	"login.failed-save-key": {
		English: "Failed to save API key for %s: %s",
		Chinese: "保存 %s 的 API 密钥失败：%s",
	},
	"login.saved-no-default": {
		English: "Saved API key for %s, but no default model is configured. Use /model.",
		Chinese: "已保存 %s 的 API 密钥，但未配置默认模型。使用 /model。",
	},
	"login.saved-select-failed": {
		English: "Saved API key for %s, but could not select the default model: %s",
		Chinese: "已保存 %s 的 API 密钥，但无法选择默认模型：%s",
	},
	"login.saved-defaults-failed": {
		English: "Saved API key for %s and selected %s, but could not save defaults: %s",
		Chinese: "已保存 %s 的 API 密钥并选择了 %s，但无法保存默认值：%s",
	},
	"login.saved-selected": {
		English: "Saved API key for %s. Selected %s. Credentials saved to %s.",
		Chinese: "已保存 %s 的 API 密钥。已选择 %s。凭据已保存到 %s。",
	},
	"login.saved-refresh-failed": {
		English: "Saved API key for %s, but could not refresh the active provider: %s",
		Chinese: "已保存 %s 的 API 密钥，但无法刷新当前提供方：%s",
	},
	"login.saved": {
		English: "Saved API key for %s. Credentials saved to %s.",
		Chinese: "已保存 %s 的 API 密钥。凭据已保存到 %s。",
	},
	"login.stop-loop": {
		English: "stop the active loop before signing in",
		Chinese: "登录前请停止活动循环",
	},
	"login.finish-queued": {
		English: "finish the queued input before signing in",
		Chinese: "登录前请完成已排队的输入",
	},
	"login.unsupported-codex-method": {
		English: "unsupported OpenAI Codex sign-in method: %s",
		Chinese: "不支持 OpenAI Codex 登录方式：%s",
	},
	"login.before-compacting": {
		English: "finish or cancel the %s sign-in before compacting",
		Chinese: "压缩前请完成或取消 %s 登录",
	},
	"login.codex-cancelling": {
		English: "Cancelling %s sign-in…",
		Chinese: "正在取消 %s 登录…",
	},
	"login.codex-input-unavailable": {
		English: "OpenAI Codex sign-in input is unavailable; cancel and start again",
		Chinese: "OpenAI Codex 登录输入不可用；请取消后重新开始",
	},
	"login.codex-accepted": {
		English: "OpenAI Codex sign-in accepted; finishing…",
		Chinese: "已接受 OpenAI Codex 登录；正在完成…",
	},
	"login.codex-completing": {
		English: "OpenAI Codex sign-in is already completing.",
		Chinese: "OpenAI Codex 登录已在完成中。",
	},
	"login.activate-model-unchanged": {
		English: "Signed in to %s. Model unchanged: %s/%s",
		Chinese: "已登录 %s。模型未变：%s/%s",
	},
	"login.activate-no-model": {
		English: "Signed in to %s, but no %s model is available: %s",
		Chinese: "已登录 %s，但没有可用的 %s 模型：%s",
	},
	"login.activate-failed": {
		English: "Signed in to %s, but model activation failed: %s. Use /model to select a model.",
		Chinese: "已登录 %s，但模型激活失败：%s。使用 /model 选择模型。",
	},
	"login.activate-session-failed": {
		English: "Signed in to %s, but the session update failed: %s",
		Chinese: "已登录 %s，但会话更新失败：%s",
	},
	"login.activate-model": {
		English: "Signed in to %s. Model: %s/%s",
		Chinese: "已登录 %s。模型：%s/%s",
	},
	"logout.use-logout": {
		English: "use /logout",
		Chinese: "使用 /logout",
	},
	"logout.none": {
		English: "No stored credentials to remove. /logout only removes entries from auth.json; environment variables and models.json config are unchanged.",
		Chinese: "没有可移除的已存凭据。/logout 只移除 auth.json 中的条目；环境变量和 models.json 配置不变。",
	},
	"logout.unsupported-provider": {
		English: "Failed to remove stored credential for %s: unsupported provider",
		Chinese: "移除 %s 的已存凭据失败：不支持的提供方",
	},
	"logout.removing": {
		English: "Removing stored OAuth credential…",
		Chinese: "正在移除已存的 OAuth 凭据…",
	},
	"logout.failed": {
		English: "Failed to remove stored %s for %s: %s",
		Chinese: "移除 %s 的已存 %s 失败：%s",
	},
	"logout.no-oauth": {
		English: "No stored OAuth credential for %s was found.",
		Chinese: "未找到 %s 的已存 OAuth 凭据。",
	},
	"logout.no-key": {
		English: "No stored API key for %s was found.",
		Chinese: "未找到 %s 的已存 API 密钥。",
	},
	"logout.removed-oauth": {
		English: "Removed stored OAuth credential for %s.\n%s",
		Chinese: "已移除 %s 的已存 OAuth 凭据。\n%s",
	},
	"logout.removed-key": {
		English: "Removed stored API key for %s.\nEnvironment variables and models.json config are unchanged.",
		Chinese: "已移除 %s 的已存 API 密钥。\n环境变量和 models.json 配置不变。",
	},
	"logout.kind-oauth": {
		English: "OAuth credential",
		Chinese: "OAuth 凭据",
	},
	"logout.kind-key": {
		English: "API key",
		Chinese: "API 密钥",
	},
	"logout.revoked-refresh": {
		English: "OpenAI accepted the refresh-token revocation request.",
		Chinese: "OpenAI 已接受刷新令牌撤销请求。",
	},
	"logout.revoked-access": {
		English: "OpenAI accepted the access-token revocation request.",
		Chinese: "OpenAI 已接受访问令牌撤销请求。",
	},
	"logout.no-token": {
		English: "No usable token; no revocation request was sent.",
		Chinese: "没有可用令牌；未发送撤销请求。",
	},
	"logout.not-attempted": {
		English: "Revocation not attempted.",
		Chinese: "未尝试撤销。",
	},
	"logout.unconfirmed": {
		English: "Revocation unconfirmed.",
		Chinese: "撤销未确认。",
	},
	"panel.enter-code": {
		English: "Enter code: %s",
		Chinese: "输入代码：%s",
	},
	"panel.heading": {
		English: "Login to OpenAI Codex",
		Chinese: "登录 OpenAI Codex",
	},
	"panel.browser-manual-above": {
		English: "The browser did not open. Open the link above manually.",
		Chinese: "浏览器未打开。请手动打开上面的链接。",
	},
	"panel.enlarge-verification": {
		English: "Enlarge the terminal for the verification link.",
		Chinese: "请放大终端以显示验证链接。",
	},
	"panel.waiting": {
		English: "Waiting for authentication…",
		Chinese: "正在等待身份验证…",
	},
	"panel.keys-open": {
		English: "Enter open browser · Esc cancel · Ctrl+D quit",
		Chinese: "Enter 打开浏览器 · Esc 取消 · Ctrl+D 退出",
	},
	"panel.keys": {
		English: "Esc cancel · Ctrl+D quit",
		Chinese: "Esc 取消 · Ctrl+D 退出",
	},
	"panel.temporary-port": {
		English: "Temporary port. If OpenAI rejects it, close the tab, cancel, and choose device code.",
		Chinese: "临时端口。如果 OpenAI 拒绝，请关闭标签页、取消，然后选择设备代码。",
	},
	"panel.browser-manual-below": {
		English: "The browser did not open. Open the link below manually.",
		Chinese: "浏览器未打开。请手动打开下面的链接。",
	},
	"panel.enlarge-signin": {
		English: "Enlarge the terminal for the sign-in link.",
		Chinese: "请放大终端以显示登录链接。",
	},
	"panel.complete": {
		English: "Complete OpenAI Codex sign-in in your browser.",
		Chinese: "请在浏览器中完成 OpenAI Codex 登录。",
	},
	"panel.paste-hint": {
		English: "Or paste redirect URL or code below · Enter submit · Esc cancel",
		Chinese: "或在下方粘贴重定向 URL 或代码 · Enter 提交 · Esc 取消",
	},
	"panel.starting": {
		English: "Starting sign-in… · Esc cancel · Ctrl+D quit",
		Chinese: "正在开始登录… · Esc 取消 · Ctrl+D 退出",
	},
}

// chineseUI reports whether app-authored UI text uses Simplified Chinese.
func (r *runtime) chineseUI() bool {
	return effectivePromptLanguage(r.options, r.settings) == config.LanguageChinese
}

// loginText resolves one key from the login catalog. Panel and selector code
// use it directly because they do not own a runtime.
func loginText(chinese bool, key i18n.Key, args ...any) string {
	text := loginCatalog.Text(chinese, key)
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}
