package i18n

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	LocaleAuto = "auto"
	LocaleEN   = "en-US"
	LocaleZH   = "zh-CN"
)

type Config struct {
	Locale     string
	CatalogDir string
}

type Catalog struct {
	messages map[string]map[string]string
	aliases  map[string]map[string][]string
}

func New(cfg Config) Catalog {
	c := Catalog{messages: map[string]map[string]string{}, aliases: map[string]map[string][]string{}}
	c.merge(LocaleEN, builtinEN)
	c.merge(LocaleZH, builtinZH)
	c.mergeAliases(LocaleEN, builtinAliasesEN)
	c.mergeAliases(LocaleZH, builtinAliasesZH)
	_ = c.LoadDir(cfg.CatalogDir)
	return c
}

func NormalizeLocale(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "", "auto":
		return LocaleAuto
	case "en", "en-us":
		return LocaleEN
	case "zh", "zh-cn", "cn":
		return LocaleZH
	default:
		return strings.TrimSpace(locale)
	}
}

func ResolveLocale(configured, userText string) string {
	locale := NormalizeLocale(configured)
	if locale != LocaleAuto {
		return locale
	}
	if ContainsHan(userText) {
		return LocaleZH
	}
	return LocaleEN
}

func ContainsHan(text string) bool {
	for _, r := range text {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

func (c Catalog) T(locale, key string, values map[string]string) string {
	locale = ResolveLocale(locale, "")
	text := c.lookup(locale, key)
	if text == "" && locale != LocaleEN {
		text = c.lookup(LocaleEN, key)
	}
	if text == "" {
		text = key
	}
	for k, v := range values {
		text = strings.ReplaceAll(text, "{"+k+"}", v)
	}
	return text
}

func (c Catalog) MatchAlias(locale, text string, actions ...string) (string, bool) {
	normalized := NormalizeAlias(text)
	if normalized == "" {
		return "", false
	}
	for _, candidateLocale := range aliasLocaleFallbacks(locale) {
		for _, action := range actions {
			if c.aliasMatches(candidateLocale, action, normalized) {
				return action, true
			}
		}
	}
	return "", false
}

func (c Catalog) LoadDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".yaml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return err
		}
		c.mergeRaw(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), raw)
	}
	return nil
}

func (c Catalog) lookup(locale, key string) string {
	if c.messages == nil {
		return ""
	}
	return c.messages[locale][key]
}

func (c Catalog) merge(locale string, messages map[string]string) {
	locale = NormalizeLocale(locale)
	if locale == LocaleAuto {
		return
	}
	if c.messages[locale] == nil {
		c.messages[locale] = map[string]string{}
	}
	for key, value := range messages {
		c.messages[locale][key] = value
	}
}

func (c Catalog) mergeRaw(locale string, raw map[string]any) {
	messages := map[string]string{}
	aliases := map[string][]string{}
	for key, value := range raw {
		key = strings.TrimSpace(key)
		if strings.HasPrefix(key, "aliases.") {
			action := strings.TrimPrefix(key, "aliases.")
			aliases[action] = append(aliases[action], stringsFromYAML(value)...)
			continue
		}
		if text, ok := value.(string); ok {
			messages[key] = text
		}
	}
	c.merge(locale, messages)
	c.mergeAliases(locale, aliases)
}

func (c Catalog) mergeAliases(locale string, aliases map[string][]string) {
	locale = NormalizeLocale(locale)
	if locale == LocaleAuto {
		return
	}
	if c.aliases[locale] == nil {
		c.aliases[locale] = map[string][]string{}
	}
	for action, values := range aliases {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		c.aliases[locale][action] = append(c.aliases[locale][action], values...)
	}
}

func (c Catalog) aliasMatches(locale, action, normalized string) bool {
	if c.aliases == nil {
		return false
	}
	for _, alias := range c.aliases[locale][action] {
		if NormalizeAlias(alias) == normalized {
			return true
		}
	}
	return false
}

func NormalizeAlias(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

func aliasLocaleFallbacks(locale string) []string {
	locale = ResolveLocale(locale, "")
	if locale == LocaleEN {
		return []string{LocaleEN, LocaleZH}
	}
	return []string{locale, LocaleEN, LocaleZH}
}

func stringsFromYAML(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

var builtinAliasesEN = map[string][]string{
	"confirm":       {"confirm", "approve", "yes", "y", "ok", "continue"},
	"cancel":        {"cancel", "reject", "stop", "no", "n"},
	"memory_commit": {"save", "remember", "commit", "confirm", "yes", "y", "ok"},
	"memory_reject": {"ignore", "skip", "reject", "cancel", "no", "n"},
	"promote":       {"promote", "apply", "commit", "confirm", "yes", "y", "ok"},
	"reject":        {"ignore", "skip", "reject", "cancel", "no", "n"},
	"run":           {"run", "test", "execute", "confirm", "yes", "y", "ok"},
}

var builtinAliasesZH = map[string][]string{
	"confirm":       {"确认", "同意", "继续"},
	"cancel":        {"取消", "不要", "放弃"},
	"memory_commit": {"保存", "保存记忆", "保存到长期记忆", "写入", "写入记忆"},
	"memory_reject": {"忽略", "不保存", "不要保存", "跳过", "放弃"},
	"promote":       {"确认", "保存", "生效", "应用"},
	"reject":        {"忽略", "取消", "不保存", "不要保存", "跳过", "放弃"},
	"run":           {"执行", "试运行", "现在执行", "现在试运行", "跑一下", "运行", "确认", "继续"},
}

var builtinEN = map[string]string{
	"approval.confirm.reason":           "This command may be destructive. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"approval.confirm.resume_dangerous": "Continue after confirming the dangerous command",
	"approval.confirm.generic":          "Confirmation is required before continuing. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"approval.confirm.resume_tool":      "Continue after confirming {tool}",
	"agent_profile.review.question":     "Detected an agent core md draft. Reply \"confirm\" to promote it, or \"ignore\" to reject it. You can also send a new task.",
	"agent_profile.review.pending":      "This agent core md draft is still waiting for review. Reply \"confirm\" to promote it, or \"ignore\" to reject it. You can also send a new task.",
	"agent_profile.promote.error":       "Failed to promote the core md draft: {error}",
	"agent_profile.promote.done":        "Promoted the agent core md draft: {target}\nBackup: {backup}",
	"agent_profile.reject.error":        "Failed to ignore the core md draft: {error}",
	"agent_profile.reject.done":         "Ignored this agent core md draft.",
	"memory.review.question":            "Reply \"save\" to write this to long-term memory, or \"ignore\" to reject this candidate.",
	"memory.commit.error":               "Failed to save long-term memory: {error}",
	"memory.commit.done":                "Saved to long-term memory: {target}",
	"memory.reject.error":               "Failed to ignore the long-term memory candidate: {error}",
	"memory.reject.done":                "Ignored this long-term memory candidate.",
	"schedule.review.question":          "The scheduled task is recorded and waiting for a test run. Reply \"run\" to test it now; I will activate it after a successful test. You can also run it later: `mateway schedule test {schedule_id}`.",
	"schedule.review.pending":           "This scheduled task is still waiting for a test run. Reply \"run\" to test it now, or \"cancel\" to discard it. You can also run `mateway schedule test {schedule_id}` later.",
	"schedule.cancel.error":             "Failed to cancel the scheduled task: {error}",
	"schedule.cancel.done":              "Cancelled this scheduled task waiting for a test run.",
	"schedule.test.error":               "The test run failed, so the scheduled task was not activated: {error}",
	"schedule.test.done":                "The test run succeeded. Scheduled task added: {task_id}. Next run: {run_at}",
	"feishu.title.approval_pending":     "Mateway Waiting for Confirmation",
	"feishu.title.input_required":       "Mateway Needs More Information",
	"feishu.title.error":                "Mateway Failed",
	"feishu.title.default":              "Mateway",
	"feishu.button.confirm":             "Approve",
	"feishu.button.cancel":              "Reject",
	"feishu.footer.approval_pending":    "You can also reply directly with \"confirm\" or \"cancel\".",
	"feishu.footer.input_required":      "Please reply directly with the missing information.",
	"feishu.footer.error":               "The task stopped at a safe point. You can add more information and retry.",
	"feishu.footer.default":             "Status: {status}",
	"feishu.fallback.approval_pending":  "I need your confirmation before continuing. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"feishu.fallback.input_required":    "I need one more piece of information before I can continue.",
	"feishu.fallback.error":             "The task failed and stopped at a safe point.",
	"feishu.fallback.default":           "Completed.",
}

var builtinZH = map[string]string{
	"approval.confirm.reason":           "这个命令可能有破坏性。回复“确认”继续，或回复“取消”放弃。",
	"approval.confirm.resume_dangerous": "确认后继续执行危险命令",
	"approval.confirm.generic":          "继续之前需要确认。回复“确认”继续，或回复“取消”放弃。",
	"approval.confirm.resume_tool":      "确认后继续执行 {tool}",
	"agent_profile.review.question":     "检测到 agent 核心 md 修改草稿。回复“确认”让它生效，回复“忽略”放弃；也可以继续发新任务。",
	"agent_profile.review.pending":      "这个 agent 核心 md 草稿还在等待审核。回复“确认”生效，回复“忽略”放弃；也可以直接发新任务。",
	"agent_profile.promote.error":       "核心 md 草稿生效失败：{error}",
	"agent_profile.promote.done":        "已生效 agent 核心 md 草稿：{target}\n备份：{backup}",
	"agent_profile.reject.error":        "忽略核心 md 草稿失败：{error}",
	"agent_profile.reject.done":         "已忽略这个 agent 核心 md 草稿。",
	"memory.review.question":            "回复“保存”写入长期记忆，或回复“忽略”放弃这条候选。",
	"memory.commit.error":               "保存长期记忆失败：{error}",
	"memory.commit.done":                "已保存到长期记忆：{target}",
	"memory.reject.error":               "忽略长期记忆候选失败：{error}",
	"memory.reject.done":                "已忽略这条长期记忆候选。",
	"schedule.review.question":          "定时任务已记录为待试运行。回复“执行”现在试运行；试运行成功后我会激活它。也可以稍后手动执行：`mateway schedule test {schedule_id}`。",
	"schedule.review.pending":           "这个定时任务还在等待试运行。回复“执行”现在试运行，回复“取消”放弃；也可以稍后手动执行 `mateway schedule test {schedule_id}`。",
	"schedule.cancel.error":             "取消定时任务失败：{error}",
	"schedule.cancel.done":              "已取消这个待试运行的定时任务。",
	"schedule.test.error":               "试运行失败，定时任务没有激活：{error}",
	"schedule.test.done":                "试运行成功，已添加定时任务：{task_id}，下次运行时间：{run_at}",
	"feishu.title.approval_pending":     "Mateway 等待确认",
	"feishu.title.input_required":       "Mateway 需要补充信息",
	"feishu.title.error":                "Mateway 执行失败",
	"feishu.title.default":              "Mateway",
	"feishu.button.confirm":             "同意",
	"feishu.button.cancel":              "拒绝",
	"feishu.footer.approval_pending":    "也可以直接回复“确认”或“取消”。",
	"feishu.footer.input_required":      "请直接回复消息补充所需信息。",
	"feishu.footer.error":               "任务已停在安全位置，可以补充信息后重试。",
	"feishu.footer.default":             "状态：{status}",
	"feishu.fallback.approval_pending":  "继续之前需要你确认。回复“确认”继续，或回复“取消”放弃。",
	"feishu.fallback.input_required":    "我还需要你补充一个信息才能继续。",
	"feishu.fallback.error":             "任务失败了，我已经停在安全位置。",
	"feishu.fallback.default":           "完成。",
}
