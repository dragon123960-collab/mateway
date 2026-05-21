# 飞书回归-内部结构泄漏与 EOF

## 问题
对照飞书实机截图，确认 followup 场景和危险命令场景的当前输出质量。

## 结果
- followup 场景中，飞书最终回复仍可能直接暴露底层 `plan failed: Post ... unexpected EOF`。
- followup 场景中，飞书最终回复仍可能直接暴露内部 plan/tool/args/risk 的 JSON 结构。
- 危险命令场景的确认边界正常，能进入中文确认提示。

## 结论
任务机制基本通，但飞书最终出站清洗还不完整，下一步应优先修复回复包装和 sanitizer 覆盖面。

## Skills
- stage: planning
  - fresh-search
    - reason: when_contains
    - dir: /Users/dongping/.mateway/workspace/skills/fresh-search
- stage: synthesis

## 执行过程与参数
- trace_id: feishu-om_x100b6fe17177ac98b2f40526081d5eb
- session_key: feishu:oc_afc228b7734c68f8ea5863fdae50f57a
- channel: feishu
- user_id: local
- thread_id: feishu-thread
- home: /Users/dongping/.mateway
- project_root: /Users/dongping/project/mateway
- generated_at: 2026-05-20T14:27:00+08:00
- trace_file: /Users/dongping/.mateway/trace/events-2026-05-20.jsonl

### Trace Evidence
- runtime.followup_resolved: active_followup
- runtime.failed: plan failed: Post "https://api.minimaxi.com/anthropic/v1/messages": unexpected EOF
- runtime.tool_start: shell.run requires_confirm=true for rm -rf
- runtime.tool_done: await_confirm
