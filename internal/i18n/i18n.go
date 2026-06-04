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
	"approve":       {"approve", "confirm", "yes", "y", "ok"},
	"cancel":        {"cancel", "reject", "stop", "no", "n"},
	"continue":      {"continue", "go on", "proceed", "execute", "run it", "yes continue"},
	"ignore":        {"ignore", "skip", "reject", "discard"},
	"memory_commit": {"save", "remember", "commit", "confirm", "yes", "y", "ok"},
	"memory_reject": {"ignore", "skip", "reject", "cancel", "no", "n"},
	"promote":       {"promote", "apply", "commit", "confirm", "yes", "y", "ok"},
	"reject":        {"ignore", "skip", "reject", "cancel", "no", "n"},
	"run":           {"run", "test", "execute", "confirm", "yes", "y", "ok"},
}

var builtinAliasesZH = map[string][]string{
	"confirm":       {"确认", "同意", "继续"},
	"approve":       {"确认", "同意", "可以", "是的", "好", "好的"},
	"cancel":        {"取消", "不要", "放弃"},
	"continue":      {"继续", "接着", "执行", "继续执行", "开始", "开始执行"},
	"ignore":        {"忽略", "跳过", "放弃", "拒绝"},
	"memory_commit": {"保存", "保存记忆", "保存到长期记忆", "写入", "写入记忆"},
	"memory_reject": {"忽略", "不保存", "不要保存", "跳过", "放弃"},
	"promote":       {"确认", "保存", "生效", "应用"},
	"reject":        {"忽略", "取消", "不保存", "不要保存", "跳过", "放弃"},
	"run":           {"执行", "测试", "试运行", "现在执行", "现在试运行", "跑一下", "运行", "确认", "继续"},
}

var builtinEN = map[string]string{
	"runtime.context_budget_exceeded":   "The current session context is still too large, so I stopped this request. Send `/new` to start a clean session; the old session will be archived automatically.",
	"runtime.activity_timeout":          "The task had no new model, tool, or hook activity for a long time, so I stopped it to avoid hanging. The trace recorded task_inactivity_timeout.",
	"runtime.partial.empty":             "The task is not complete: there was not enough progress to continue.",
	"runtime.partial.prefix":            "The task is not complete. Current progress:\n\n{text}",
	"runtime.session_reset.done":        "Started a new session.",
	"runtime.session_reset.archived":    "Old session archived: {archive_path}",
	"runtime.archive_recall.question":   "Do you want to resume this archived task?\n\n- {goal}\n\nReply \"confirm\" to create a new task in the current session using that archived task as context, or reply \"cancel\" to skip it.",
	"runtime.archive_recall.candidates": "I found multiple possible archived tasks. Please clarify which one you want to resume:\n",
	"runtime.archive_recall.cancelled":  "Cancelled archived task recall.",
	"runtime.archive_recall.load_error": "Failed to read archived task: {error}",
	"runtime.archive_recall.missing":    "I could not find that task in the archive. Please describe which task you want to resume again.",
	"runtime.cancelled":                 "Cancelled.",
	"runtime.invalid_tool_call":         "The model generated an invalid tool call format, so I stopped to avoid an unsafe operation. Please retry or describe the task more specifically.",
	"runtime.empty_reply":               "I have not generated a usable reply yet.",
	"runtime.tool_failure_loop":         "Tools returned repeated failures or suspicious results, so I stopped to avoid looping on the wrong path.",
	"runtime.error.timeout":             "The model service timed out this time, and the task stopped at a safe point. You can reply \"retry\" or send the question again, and I will continue from the current context.",
	"runtime.error.missing_api_key":     "The current model configuration is missing an API key, so the task did not continue. Please check the model configuration and retry.",
	"runtime.error.all_models_failed":   "All available models failed, and the task stopped at a safe point. You can reply \"retry\" later, or switch/check the fallback model configuration.",
	"runtime.error.generic":             "The task failed and stopped at a safe point. You can add more information and retry.",
	"runtime.heuristic.echo":            "Received: {text}",
	"gateway.media_download_failed":     "Image download failed: {error}",
	"gateway.processing_ack":            "Received. Processing now.",
	"gateway.processing_failed":         "Processing failed: {error}",
	"router.followup.cues.followup":     "continue,expand,same task",
	"router.followup.cues.new_task":     "start a new task,new task",
	"router.followup.cues.historical":   "previous task,earlier task",
	"router.followup.cues.retry":        "retry,try again",
	"router.followup.cues.short_suffix": "",
	"router.followup.cues.weak":         "",
	"router.followup.clarify":           "I cannot reliably tell which historical task you want to resume yet. Please add a clearer clue, such as task number, topic, file name, or keyword.",
	"router.completion.incomplete_cues": "next i will,i will now,will proceed,will continue,continue now,start writing,start creating,start sending,check the script",
	"router.completion.ack_exact":       "received,noted,executing,running,starting,will do,on it",
	"router.completion.ack_short":       "execut,running,starting",
	"router.action.info_cues":           "remember,read,summarize,check,search",
	"router.action.action_cues":         "create,write,modify,update,delete,remove,send,publish,deploy,run,execute,restart,close,open,install,configure,submit",
	"router.action.generate_cues":       "generate",
	"router.action.generated_artifacts": "markdown,.md,.html,.json,.yaml,file,report,document",
	"router.action.ack_exact":           "confirm,continue,yes,y",
	"router.action.ack_contains":        "",
	"router.blocker.cues":               "api key,permission denied,missing,not found,cannot access,need you to provide,need user input,requires confirmation,blocked",
	"router.input_request.contains":     "please provide,please clarify,need you to provide,do you need me to,would you like me to,missing input",
	"router.input_request.question":     "which,what",
	"router.standalone_task.verbs":      "read,summarize,check,search,create,generate,list",
	"router.standalone_task.markers":    "readme,.md,.txt,.json,.yaml,.yml,/,~",
	"router.warning.malformed_cues":     "malformed",
	"router.partial.already_marked":     "not complete",
	"router.archive_recall.cues":        "",
	"router.unexecuted.pending_phrases": "chmod",
	"router.unexecuted.action_cues":     "now,next,i will,let me,going to",
	"router.unexecuted.work_cues":       "chmod,test,verify,run,execute,send,write,create",
	"agent_profile.unsafe_markers":      "[tool_call],[/tool_call],<system>,</system>,role: system,role: assistant,ignore previous instructions",
	"memory.learning.strong_cues":       "remember,memory,preference,rule,decision,lesson,workflow,next time",
	"memory.learning.correction_cues":   "wrong,incorrect,should,next time don't,do not do that,correction",
	"memory.learning.not_found_cues":    "not found,missing",
	"memory.learning.permission_cues":   "denied,permission",
	"memory.distill.score_cues":         "failed,error,preference,remember",
	"memory.safe_read.markers":          "memory,remember,preference,project,readme,tool",
	"approval.confirm.reason":           "This command may be destructive. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"approval.confirm.resume_dangerous": "Continue after confirming the dangerous command",
	"approval.confirm.generic":          "Confirmation is required before continuing. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"approval.confirm.resume_tool":      "Continue after confirming {tool}",
	"approval.confirm.external_script":  "External skill script {script} requires one-time authorization before execution.",
	"approval.confirm.resume_script":    "Authorize the script, then continue the original task. Re-plan the script.run arguments from the user request; do not treat a previous --help/probe call as the task execution.",
	"agent_profile.review.question":     "Detected an agent core md draft. Reply \"confirm\" to promote it, or \"ignore\" to reject it. You can also send a new task.",
	"agent_profile.review.pending":      "This agent core md draft is still waiting for review. Reply \"confirm\" to promote it, or \"ignore\" to reject it. You can also send a new task.",
	"agent_profile.promote.error":       "Failed to promote the core md draft: {error}",
	"agent_profile.promote.done":        "Promoted the agent core md draft: {target}\nBackup: {backup}",
	"agent_profile.reject.error":        "Failed to ignore the core md draft: {error}",
	"agent_profile.reject.done":         "Ignored this agent core md draft.",
	"memory.review.question":            "Reply \"save\" to write this to long-term memory, or \"ignore\" to reject this candidate.",
	"memory.proposal_review.header":     "I found a long-term memory candidate that has not been written yet.\n\n",
	"memory.proposal_review.type":       "\nType: ",
	"memory.proposal_review.confidence": ", confidence: ",
	"memory.proposal_review.summary":    "\nSummary: ",
	"memory.proposal_review.sources":    "\nSources: ",
	"memory.proposal_review.show":       "\n\nDetails: `mateway memory proposal show {proposal_id}`",
	"memory.proposal_review.commit":     "\nSave: `mateway memory proposal commit {proposal_id}`",
	"memory.proposal_review.reject":     "\nIgnore: `mateway memory proposal reject {proposal_id}`",
	"memory.proposal_review.reply":      "\n\nYou can also reply directly with `save` or `ignore`.",
	"memory.proposal_nudge.header":      "There are {total} long-term memory candidates waiting for review. Here are the {shown} most useful ones:",
	"memory.proposal_nudge.type":        "   Type: {type} / {scope}, confidence: {confidence}\n",
	"memory.proposal_nudge.value":       "   Value: {value}\n",
	"memory.proposal_nudge.sources":     "   Sources: {sources}\n",
	"memory.proposal_nudge.show":        "   Details: `mateway memory proposal show {proposal_id}`",
	"memory.proposal_nudge.more":        "\n\n{rest} more not shown. View all: `mateway memory proposal list`",
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
	"schedule.test.summary":             "Scheduled task test run succeeded: {task_id}",
	"feishu.title.approval_pending":     "Mateway Waiting for Confirmation",
	"feishu.title.input_required":       "Mateway Needs More Information",
	"feishu.title.error":                "Mateway Failed",
	"feishu.title.default":              "Mateway",
	"feishu.button.confirm":             "Approve",
	"feishu.button.cancel":              "Reject",
	"feishu.approval_text.confirm_cues": "confirm,approve,确认,同意",
	"feishu.approval_text.cancel_cues":  "cancel,stop,取消,放弃",
	"feishu.footer.approval_pending":    "You can also reply directly with \"confirm\" or \"cancel\".",
	"feishu.footer.partial":             "Status: partial",
	"feishu.footer.input_required":      "Please reply directly with the missing information.",
	"feishu.footer.error":               "The task stopped at a safe point. You can add more information and retry.",
	"feishu.footer.default":             "Status: {status}",
	"feishu.fallback.approval_pending":  "I need your confirmation before continuing. Reply \"confirm\" to continue, or \"cancel\" to stop.",
	"feishu.fallback.input_required":    "I need one more piece of information before I can continue.",
	"feishu.fallback.error":             "The task failed and stopped at a safe point.",
	"feishu.fallback.default":           "Completed.",
	"router.pending_intent.examples":    "{\"question\":\"Should I run the remaining steps?\",\"message\":\"yes, continue\",\"kind\":\"action_ack\"}\n{\"question\":\"Which directory should I summarize?\",\"message\":\"docs/\",\"kind\":\"answer_pending\"}\n{\"question\":\"Which directory should I summarize?\",\"message\":\"Read README.md and summarize the project.\",\"kind\":\"new_task\"}\n{\"question\":\"需要我以正确的参数重新调用图像生成工具吗？\",\"message\":\"需要\",\"kind\":\"action_ack\"}",
	"aliases.confirm.primary":           "confirm",
	"aliases.cancel.primary":            "cancel",
	"aliases.confirm":                   "confirm,approve,yes,y,ok",
	"aliases.approve":                   "approve,confirm,yes,y,ok",
	"aliases.cancel":                    "cancel,deny,no,n,stop",
	"aliases.continue":                  "continue,go on,proceed,execute,run it,yes continue",
	"aliases.run":                       "run,test,try,execute",
	"aliases.ignore":                    "ignore,skip,reject,discard",
}

var builtinZH = map[string]string{
	"runtime.context_budget_exceeded":   "当前会话上下文仍然过大，已停止这次请求。请发送 `/new` 开启干净会话，旧会话会自动归档。",
	"runtime.activity_timeout":          "任务长时间没有新的模型、工具或 hook 活动，已停止执行，避免挂死。Trace 中已记录 task_inactivity_timeout。",
	"runtime.partial.empty":             "任务还没有完成：当前没有足够进展继续执行。",
	"runtime.partial.prefix":            "任务还没有完成，当前进展：\n\n{text}",
	"runtime.session_reset.done":        "已开启新会话。",
	"runtime.session_reset.archived":    "旧会话已归档：{archive_path}",
	"runtime.archive_recall.question":   "你是想接回这个已归档任务吗？\n\n- {goal}\n\n回复“确认”会在当前新会话里创建一个新任务，并引用这个归档任务作为上下文；回复“取消”则不接回。",
	"runtime.archive_recall.candidates": "我找到了多个可能的归档任务，请补充你要接哪一个：\n",
	"runtime.archive_recall.cancelled":  "已取消接回归档任务。",
	"runtime.archive_recall.load_error": "读取归档任务失败：{error}",
	"runtime.archive_recall.missing":    "归档里没有找到这个任务，请重新说明要接哪个任务。",
	"runtime.cancelled":                 "已取消。",
	"runtime.invalid_tool_call":         "模型生成了无效的工具调用格式，已停止执行，避免误操作。请重试或把任务说得更具体。",
	"runtime.empty_reply":               "我还没有生成可用回复。",
	"runtime.tool_failure_loop":         "工具连续返回失败或可疑结果，已停止执行，避免在错误路径上无限循环。",
	"runtime.error.timeout":             "模型服务这次响应超时了，任务已经停在安全位置。你可以直接回复“重试”或把问题再发一遍，我会接着当前上下文继续。",
	"runtime.error.missing_api_key":     "当前模型配置缺少 API Key，任务没有继续执行。请检查模型配置后重试。",
	"runtime.error.all_models_failed":   "当前可用模型都调用失败了，任务已经停在安全位置。你可以稍后回复“重试”，或切换/检查 fallback 模型配置。",
	"runtime.error.generic":             "任务执行失败了，已经停在安全位置。你可以补充信息后重试。",
	"runtime.heuristic.echo":            "收到：{text}",
	"gateway.media_download_failed":     "图片下载失败：{error}",
	"gateway.processing_ack":            "收到，开始处理。",
	"gateway.processing_failed":         "处理失败：{error}",
	"router.followup.cues.followup":     "继续,接着,再,补充,扩展,改成,换成,上一个,上一条,刚才,那个,继续上面,continue,expand,same task",
	"router.followup.cues.new_task":     "新任务,另一个任务,换个话题,重新开始,不用接上,不要接上,start a new task,new task",
	"router.followup.cues.historical":   "历史,之前,前面,回到,那个任务,那件事,刚才那个,previous task,earlier task",
	"router.followup.cues.retry":        "重试,再试,再来一次,重新试,retry,try again",
	"router.followup.cues.short_suffix": "呢,吗,么,如何,怎么样",
	"router.followup.cues.weak":         "上一轮,上一个,上一条,刚才,那个,那三个,那几点",
	"router.followup.clarify":           "我还不能稳定判断你要接哪一个历史任务。请补充更明确的线索，比如第几个任务、主题、文件名或关键词。",
	"router.completion.incomplete_cues": "接下来,下一步,然后我会,我将,准备开始,并行,继续处理,环境摸清,环境梳清,先摸清,先生成,重写脚本,检查脚本,预计,计划,next i will,i will now,will proceed,will continue,continue now,start writing,start creating,start sending,check the script",
	"router.completion.ack_exact":       "收到,好的,执行,运行,开始执行,开始处理,收到，开始处理,收到,开始处理,好的，马上执行,好的,马上执行,马上执行,这就执行,我来执行,received,noted,executing,running,starting,will do,on it",
	"router.completion.ack_short":       "执行,运行,开始处理,马上,execut,running,starting",
	"router.action.info_cues":           "读取,总结,查看,检查,搜索,记住,remember,read,summarize,check,search",
	"router.action.action_cues":         "创建,写,改,修改,更新,删除,移除,发送,发布,部署,执行,运行,重启,关闭,打开,安装,配置,提交,push,commit,create,write,modify,update,delete,remove,send,publish,deploy,run,execute,restart,close,open,install,configure,submit",
	"router.action.generate_cues":       "生成,generate",
	"router.action.generated_artifacts": "文件,报告,文档,markdown,.md,.html,.json,.yaml,file,report,document",
	"router.action.ack_exact":           "确认,confirm,继续,continue,需要,要,是的,yes,y",
	"router.action.ack_contains":        "你来执行,执行剩下,继续执行",
	"router.blocker.cues":               "需要你提供,需要用户,缺少,没有权限,权限不足,无法访问,不能访问,找不到,api key,permission denied,missing,not found,cannot access,need you to provide,need user input,requires confirmation,blocked",
	"router.input_request.contains":     "需要你,请提供,请补充,please provide,please clarify,need you to provide,do you need me to,would you like me to,missing input",
	"router.input_request.question":     "哪个,什么,是否,需要我,which,what",
	"router.standalone_task.verbs":      "请读取,请总结,请查看,请检查,请搜索,请创建,请生成,请列出,帮我读取,帮我总结,帮我查看,帮我检查,帮我搜索,帮我创建,帮我生成,读取,总结,查看,检查,搜索,创建,生成,列出,read,summarize,check,search,create,generate,list",
	"router.standalone_task.markers":    "readme,.md,.txt,.json,.yaml,.yml,/,~,项目,文件,目录,邮件,网页,网站",
	"router.warning.malformed_cues":     "工具调用格式无效,malformed",
	"router.partial.already_marked":     "任务还没有完成,not complete",
	"router.archive_recall.cues":        "刚才,上个,之前",
	"router.unexecuted.pending_phrases": "给脚本添加可执行权限,添加可执行权限,chmod,开始测试,进行测试,测试发送,测试脚本",
	"router.unexecuted.action_cues":     "现在,接下来,下一步,然后,准备,开始,now,next,i will,let me,going to",
	"router.unexecuted.work_cues":       "测试,验证,执行,运行,发送,写入,创建,添加,注册,重启,chmod,test,verify,run,execute,send,write,create",
	"agent_profile.unsafe_markers":      "[tool_call],[/tool_call],<system>,</system>,role: system,role: assistant,ignore previous instructions,忽略之前,无视之前",
	"memory.learning.strong_cues":       "记住,记忆,长期,偏好,规则,决定,经验,流程,以后,remember,memory,preference,rule,decision,lesson,workflow,next time",
	"memory.learning.correction_cues":   "不是这样,不对,应该,以后别,以后不要,改成,纠正,修正,wrong,incorrect,should,next time don't,do not do that,correction",
	"memory.learning.not_found_cues":    "不存在,not found,missing",
	"memory.learning.permission_cues":   "权限,denied,permission",
	"memory.distill.score_cues":         "纠正,以后不要,以后要,修复,失败,failed,error,preference,remember",
	"memory.safe_read.markers":          "memory,remember,preference,project,readme,tool,记忆,偏好,项目,工具",
	"approval.confirm.reason":           "这个命令可能有破坏性。回复“确认”继续，或回复“取消”放弃。",
	"approval.confirm.resume_dangerous": "确认后继续执行危险命令",
	"approval.confirm.generic":          "继续之前需要确认。回复“确认”继续，或回复“取消”放弃。",
	"approval.confirm.resume_tool":      "确认后继续执行 {tool}",
	"approval.confirm.external_script":  "外部 skill 脚本 {script} 首次执行前需要一次性授权。",
	"approval.confirm.resume_script":    "授权脚本后继续原任务。需要根据用户请求重新规划 script.run 参数，不要把之前的 --help/探测调用当作任务执行。",
	"agent_profile.review.question":     "检测到 agent 核心 md 修改草稿。回复“确认”让它生效，回复“忽略”放弃；也可以继续发新任务。",
	"agent_profile.review.pending":      "这个 agent 核心 md 草稿还在等待审核。回复“确认”生效，回复“忽略”放弃；也可以直接发新任务。",
	"agent_profile.promote.error":       "核心 md 草稿生效失败：{error}",
	"agent_profile.promote.done":        "已生效 agent 核心 md 草稿：{target}\n备份：{backup}",
	"agent_profile.reject.error":        "忽略核心 md 草稿失败：{error}",
	"agent_profile.reject.done":         "已忽略这个 agent 核心 md 草稿。",
	"memory.review.question":            "回复“保存”写入长期记忆，或回复“忽略”放弃这条候选。",
	"memory.proposal_review.header":     "我发现一条长期记忆候选，尚未写入长期记忆。\n\n",
	"memory.proposal_review.type":       "\n类型：",
	"memory.proposal_review.confidence": "，置信度：",
	"memory.proposal_review.summary":    "\n摘要：",
	"memory.proposal_review.sources":    "\n来源：",
	"memory.proposal_review.show":       "\n\n查看详情：`mateway memory proposal show {proposal_id}`",
	"memory.proposal_review.commit":     "\n保存：`mateway memory proposal commit {proposal_id}`",
	"memory.proposal_review.reject":     "\n忽略：`mateway memory proposal reject {proposal_id}`",
	"memory.proposal_review.reply":      "\n\n也可以直接回复 `保存` 或 `忽略`。",
	"memory.proposal_nudge.header":      "有 {total} 条长期记忆候选待审核，我先列 {shown} 条最值得看的：",
	"memory.proposal_nudge.type":        "   类型：{type} / {scope}，置信度：{confidence}\n",
	"memory.proposal_nudge.value":       "   价值：{value}\n",
	"memory.proposal_nudge.sources":     "   来源：{sources}\n",
	"memory.proposal_nudge.show":        "   查看：`mateway memory proposal show {proposal_id}`",
	"memory.proposal_nudge.more":        "\n\n还有 {rest} 条未展示。查看全部：`mateway memory proposal list`",
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
	"schedule.test.summary":             "定时任务试运行成功：{task_id}",
	"feishu.title.approval_pending":     "Mateway 等待确认",
	"feishu.title.input_required":       "Mateway 需要补充信息",
	"feishu.title.error":                "Mateway 执行失败",
	"feishu.title.default":              "Mateway",
	"feishu.button.confirm":             "同意",
	"feishu.button.cancel":              "拒绝",
	"feishu.approval_text.confirm_cues": "确认,同意,confirm,approve",
	"feishu.approval_text.cancel_cues":  "取消,放弃,cancel,stop",
	"feishu.footer.approval_pending":    "也可以直接回复“确认”或“取消”。",
	"feishu.footer.partial":             "状态：未完成",
	"feishu.footer.input_required":      "请直接回复消息补充所需信息。",
	"feishu.footer.error":               "任务已停在安全位置，可以补充信息后重试。",
	"feishu.footer.default":             "状态：{status}",
	"feishu.fallback.approval_pending":  "继续之前需要你确认。回复“确认”继续，或回复“取消”放弃。",
	"feishu.fallback.input_required":    "我还需要你补充一个信息才能继续。",
	"feishu.fallback.error":             "任务失败了，我已经停在安全位置。",
	"feishu.fallback.default":           "完成。",
	"router.pending_intent.examples":    "{\"question\":\"需要我以正确的参数重新调用图像生成工具吗？\",\"message\":\"需要\",\"kind\":\"action_ack\"}\n{\"question\":\"要总结哪个目录？\",\"message\":\"docs/\",\"kind\":\"answer_pending\"}\n{\"question\":\"要总结哪个目录？\",\"message\":\"请读取 README.md，总结项目。\",\"kind\":\"new_task\"}\n{\"question\":\"Should I run the remaining steps?\",\"message\":\"yes, continue\",\"kind\":\"action_ack\"}",
	"aliases.confirm.primary":           "确认",
	"aliases.cancel.primary":            "取消",
	"aliases.confirm":                   "确认,同意,可以,是的,好,好的,yes,y,confirm,approve",
	"aliases.approve":                   "确认,同意,可以,是的,好,好的,yes,y,confirm,approve",
	"aliases.cancel":                    "取消,放弃,不要,不同意,否,no,n,cancel,deny,stop",
	"aliases.continue":                  "继续,接着,执行,继续执行,开始,开始执行,yes continue,continue,proceed",
	"aliases.run":                       "执行,运行,测试,试运行,跑一下,试一下,run,test,execute",
	"aliases.ignore":                    "忽略,跳过,放弃,拒绝,ignore,skip,reject,discard",
}
