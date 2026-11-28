package app

import (
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/i18n"
)

// settingsCatalog keeps the settings picker's English and Simplified Chinese
// text together. Row keys use the stable settings item ID, for example
// "settings.auto-compact.label"; those IDs also drive persistence and tests.
var settingsCatalog = i18n.Catalog{
	"settings.auto-compact.label":       {English: "Auto-compact", Chinese: "自动压缩"},
	"settings.auto-compact.description": {English: "Compact automatically when the context fills.", Chinese: "上下文将满时自动压缩。"},
	"settings.editor-padding.label":     {English: "Editor padding", Chinese: "编辑器内边距"},
	"settings.editor-padding.description": {
		English: "Horizontal padding around the editor.",
		Chinese: "编辑器左右留白。",
	},
	"settings.output-padding.label": {English: "Output padding", Chinese: "输出间距"},
	"settings.output-padding.description": {
		English: "Blank rows between transcript blocks.",
		Chinese: "消息块之间的空行数。",
	},
	"settings.hide-thinking.label": {English: "Hide thinking", Chinese: "隐藏思考过程"},
	"settings.hide-thinking.description": {
		English: "Hide thinking blocks in assistant responses.",
		Chinese: "在助手回复中隐藏思考块。",
	},
	"settings.soft-wrap.label": {English: "Soft wrap", Chinese: "软换行"},
	"settings.soft-wrap.description": {
		English: "Let the terminal wrap committed output for clean copy.",
		Chinese: "让终端对已提交输出换行，便于完整复制。",
	},
	"settings.autocomplete-max.label": {English: "Autocomplete max items", Chinese: "自动补全条数上限"},
	"settings.autocomplete-max.description": {
		English: "Rows shown in the completion list.",
		Chinese: "补全列表显示的行数。",
	},
	"settings.code-indent.label": {English: "Code block indent", Chinese: "代码块缩进"},
	"settings.code-indent.description": {
		English: "Leading spaces inside fenced code blocks.",
		Chinese: "围栏代码块内的前导空格数。",
	},
	"settings.quiet-startup.label": {English: "Quiet startup", Chinese: "静默启动"},
	"settings.quiet-startup.description": {
		English: "Hide the startup header on the next session.",
		Chinese: "下个会话隐藏启动标题。",
	},
	"settings.theme.label": {English: "Theme", Chinese: "主题"},
	"settings.theme.description": {
		English: "Active palette, built-in or discovered.",
		Chinese: "当前调色板，内置或已发现。",
	},
	"settings.color-mode.label": {English: "Color mode", Chinese: "颜色模式"},
	"settings.color-mode.description": {
		English: "Terminal color depth; false means 256 colors.",
		Chinese: "终端色深；false 表示 256 色。",
	},
	"settings.context-usage.label": {English: "Context usage", Chinese: "上下文用量"},
	"settings.context-usage.description": {
		English: "Footer context caption: percent or compact tokens.",
		Chinese: "页脚上下文说明：百分比或紧凑 token 数。",
	},
	"settings.language.label": {English: "Language", Chinese: "语言"},
	"settings.language.description": {
		English: "Built-in prompt language.",
		Chinese: "内置提示词语言。",
	},
	"settings.language.cli-override": {
		English: "--language remains active for this session.",
		Chinese: "--language 在本次会话中仍然有效。",
	},
	"settings.model-thinking.label": {English: "Default thinking level per model", Chinese: "每个模型的默认思考级别"},
	"settings.model-thinking.description": {
		English: "Override the default thinking level for specific models",
		Chinese: "覆盖特定模型的默认思考级别",
	},
	"settings.picker.hint":       {English: "Enter/Space change · Esc close", Chinese: "Enter/空格 更改 · Esc 关闭"},
	"settings.picker.short-hint": {English: "Enter change · Esc", Chinese: "Enter 更改 · Esc"},
	"settings.value.off":         {English: "off", Chinese: "关"},
	"settings.value.on":          {English: "on", Chinese: "开"},
	"settings.value.percent":     {English: "percent", Chinese: "百分比"},
	"settings.value.tokens":      {English: "tokens", Chinese: "Token 数"},
	"settings.value.none":        {English: "none", Chinese: "未设置"},
	"settings.value.configured":  {English: "%d configured", Chinese: "已配置 %d 项"},
}

func settingsText(language string, key i18n.Key) string {
	return settingsCatalog.Text(language == config.LanguageChinese, key)
}

func settingsLabel(language, id string) string {
	return settingsText(language, i18n.Key("settings."+id+".label"))
}

func settingsDescription(language, id string) string {
	return settingsText(language, i18n.Key("settings."+id+".description"))
}

func settingsBoolText(language string, value bool) string {
	if value {
		return settingsText(language, "settings.value.on")
	}
	return settingsText(language, "settings.value.off")
}
