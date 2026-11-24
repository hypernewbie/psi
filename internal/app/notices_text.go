package app

import (
	"errors"
	"fmt"

	"github.com/hypernewbie/psi/internal/i18n"
)

// noticesCatalog holds the app-authored notice and command-error text shown
// in the terminal. Entries may contain fmt.Sprintf verbs; callers pass the
// dynamic values through localized or localizedError.
var noticesCatalog = i18n.Catalog{
	"notice.theme-watch-unavailable": {
		English: "Theme file watching is unavailable; use /reload to apply theme changes.",
		Chinese: "主题文件监视不可用；使用 /reload 应用主题更改。",
	},
	"notice.signin-saved-activation-skipped": {
		English: "%s sign-in saved, but model activation was skipped after cancel. Use /model to select a model.",
		Chinese: "已保存 %s 登录，但取消后跳过了模型激活。使用 /model 选择模型。",
	},
	"notice.signin-saved-session-changed": {
		English: "%s sign-in saved, but the session changed. Use /model to select a model.",
		Chinese: "已保存 %s 登录，但会话已更改。使用 /model 选择模型。",
	},
	"notice.signin-cancelled": {
		English: "%s sign-in cancelled.",
		Chinese: "%s 登录已取消。",
	},
	"notice.close-browser-tab": {
		English: "Close the browser tab.",
		Chinese: "请关闭浏览器标签页。",
	},
	"notice.auto-compaction-failed": {
		English: "Auto-compaction failed: %s",
		Chinese: "自动压缩失败：%s",
	},
	"notice.finish-signin-first": {
		English: "Finish or cancel the %s sign-in before submitting.",
		Chinese: "提交前请完成或取消 %s 登录。",
	},
	"notice.storage-blocked": {
		English: "Storage is blocked: %s",
		Chinese: "存储被阻塞：%s",
	},
	"notice.storage-blocked-new": {
		English: "Storage is blocked: %s (open a checked copy or /new)",
		Chinese: "存储被阻塞：%s（请打开已检查的副本或使用 /new）",
	},
	"notice.cancelling-for-compaction": {
		English: "Cancelling the run for compaction.",
		Chinese: "正在取消当前运行以进行压缩。",
	},
	"notice.resource-reload-failed": {
		English: "Resource reload failed. %s",
		Chinese: "资源重新加载失败。%s",
	},
	"notice.loop-stop-failed": {
		English: "Could not stop loop: %s",
		Chinese: "无法停止循环：%s",
	},
	"notice.no-active-loop": {
		English: "No active loop",
		Chinese: "没有活动循环",
	},
	"notice.loop-stopped": {
		English: "Loop stopped after %d iteration(s)",
		Chinese: "循环在 %d 次迭代后停止",
	},
	"notice.theme-reload-failed": {
		English: "Theme reload failed; the last valid theme remains active. %s",
		Chinese: "主题重新加载失败；仍使用上一个有效主题。%s",
	},
	"notice.signin-browser": {
		English: "Complete %s sign-in in your browser: %s",
		Chinese: "请在浏览器中完成 %s 登录：%s",
	},
	"notice.signin-browser-manual": {
		English: "The browser did not open automatically. Open the sign-in link above on this machine.",
		Chinese: "浏览器未自动打开。请在本机打开上面的登录链接。",
	},
	"notice.attachment-undelivered": {
		English: "An attachment prompt was not delivered. Phi keeps a saved copy you can restore.",
		Chinese: "附件提示未能送达。Phi 保留了可恢复的副本。",
	},
	"notice.attachment-not-sent": {
		English: "An undelivered attachment prompt was not sent. Phi keeps a saved copy you can restore.",
		Chinese: "未送达的附件提示未发送。Phi 保留了可恢复的副本。",
	},
	"notice.attachments-discarded": {
		English: "%d undelivered attachment prompt(s) were discarded when the session changed",
		Chinese: "会话更改时丢弃了 %d 个未送达的附件提示",
	},
	"notice.unsaved-output": {
		English: "UNSAVED output. Storage is blocked; do not continue this session. Open a checked copy or use /new.",
		Chinese: "输出未保存。存储被阻塞；不要继续此会话。请打开已检查的副本或使用 /new。",
	},
	"notice.unsaved-output-detail": {
		English: "UNSAVED output. Storage is blocked; do not continue this session. Open a checked copy or use /new. %s",
		Chinese: "输出未保存。存储被阻塞；不要继续此会话。请打开已检查的副本或使用 /new。%s",
	},
	"notice.run-interrupted": {
		English: "The run was interrupted. Any streamed partial response was saved with an interrupted outcome; review it before continuing.",
		Chinese: "运行已中断。任何已流式输出的部分回复都以中断结果保存；继续前请检查。",
	},
	"notice.visible-saved-cancelled": {
		English: "Visible output was saved, but the operation was cancelled.",
		Chinese: "可见输出已保存，但操作已取消。",
	},
	"notice.request-cancelled": {
		English: "Request cancelled.",
		Chinese: "请求已取消。",
	},
	"notice.retry-failed": {
		English: "Retry failed after %d attempts: %s",
		Chinese: "重试 %d 次后失败：%s",
	},
	"notice.visible-saved-incomplete": {
		English: "The visible response was saved, but the operation did not complete. %s",
		Chinese: "可见回复已保存，但操作未完成。%s",
	},
	"notice.compaction-cancelled": {
		English: "Compaction cancelled.",
		Chinese: "压缩已取消。",
	},
	"notice.compaction-not-started": {
		English: "Compaction could not start. %s",
		Chinese: "压缩无法启动。%s",
	},
	"notice.saved-session-only": {
		English: "Saved for this session only: %s",
		Chinese: "仅本会话有效：%s",
	},
	"notice.language-default-stored": {
		English: "Stored the default language; --language keeps the current prompt language.",
		Chinese: "已保存默认语言；--language 保持当前提示词语言。",
	},
	"notice.theme-default-stored": {
		English: "Stored the default theme; --use-theme keeps the current display.",
		Chinese: "已保存默认主题；--use-theme 保持当前显示。",
	},
	"notice.thinking-default-saved": {
		English: "Saved default thinking level: %s",
		Chinese: "已保存默认思考级别：%s",
	},
	"notice.invalid-model": {
		English: "invalid model %s",
		Chinese: "无效模型 %s",
	},
	"notice.finish-run-first": {
		English: "Finish or cancel the run before using this picker.",
		Chinese: "使用此选择器前请完成或取消当前运行。",
	},
	"notice.unsupported-login-provider": {
		English: "unsupported login provider: %s",
		Chinese: "不支持的登录提供方：%s",
	},
	"notice.use-settings": {
		English: "use /settings",
		Chinese: "使用 /settings",
	},
	"notice.use-loop-stop": {
		English: "use /loop-stop",
		Chinese: "使用 /loop-stop",
	},
	"notice.reloaded-resources": {
		English: "Reloaded themes, display settings, prompt inputs, skills, and queue modes",
		Chinese: "已重新加载主题、显示设置、提示词输入、技能和队列模式",
	},
	"notice.reloaded-model-catalog": {
		English: "; refreshed model catalog",
		Chinese: "；已刷新模型目录",
	},
	"notice.no-credentialed-models": {
		English: "No credentialed models. Run /login or set a provider API key.",
		Chinese: "没有可用凭据的模型。运行 /login 或设置提供方 API 密钥。",
	},
	"notice.models-json-error": {
		English: "models.json error: %s",
		Chinese: "models.json 错误：%s",
	},
	"notice.nothing-to-compact-recent": {
		English: "Nothing to compact yet. Recent conversation is already within the retained budget.",
		Chinese: "暂无可压缩内容。最近的对话已在保留预算内。",
	},
	"notice.nothing-to-compact-empty": {
		English: "Nothing to compact yet. The session has no conversation history.",
		Chinese: "暂无可压缩内容。此会话没有对话历史。",
	},
	"notice.nothing-to-compact-done": {
		English: "Nothing to compact yet. This session is already compacted.",
		Chinese: "暂无可压缩内容。此会话已压缩。",
	},
	"notice.auth-offline-suffix": {
		English: "%s (offline commands still work; /login [provider], /logout, /model to select, /session for status)",
		Chinese: "%s（离线命令仍可用；/login [provider]、/logout、/model 选择模型、/session 查看状态）",
	},
	"notice.usage-name": {
		English: "Usage: /name <name>",
		Chinese: "用法：/name <name>",
	},
	"notice.session-name-normalized": {
		English: "Session name was normalized from %q to %q",
		Chinese: "会话名称已从 %q 规范化为 %q",
	},
	"notice.error-prefix": {
		English: "error: ",
		Chinese: "错误：",
	},
	"error.unknown-command": {
		English: "unknown command",
		Chinese: "未知命令",
	},
	"error.use-reload": {
		English: "use /reload",
		Chinese: "使用 /reload",
	},
	"error.use-clone": {
		English: "use /clone",
		Chinese: "使用 /clone",
	},
	"error.use-model": {
		English: "use /model [provider/]model",
		Chinese: "使用 /model [provider/]model",
	},
	"error.use-settings": {
		English: "use /settings",
		Chinese: "使用 /settings",
	},
	"error.unknown-thinking-level": {
		English: "Unknown thinking level %q. Available levels: %s.",
		Chinese: "未知思考级别 %q。可用级别：%s。",
	},
}

// localized returns the notice or error text in the language that governs the
// prompt. Arguments follow fmt.Sprintf rules when present. Keys are looked up
// in the notice catalog first, then the login catalog.
func (r *runtime) localized(key i18n.Key, args ...any) string {
	chinese := r.chineseUI()
	text := noticesCatalog.Text(chinese, key)
	if text == string(key) {
		text = loginCatalog.Text(chinese, key)
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

func (r *runtime) localizedError(key i18n.Key, args ...any) error {
	return errors.New(r.localized(key, args...))
}
