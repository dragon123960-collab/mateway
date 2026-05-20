# Mateway 测试问题清单

更新时间：2026-05-20

这份文档只记录本轮真实模型批测中暴露出来的问题、证据和影响，不展开修复实现。

## 1. followup 续接对“继续上一轮”不稳定

状态：已处理（2026-05-20）

处理记录：

- 已在 runtime task binding 中接入 `internal/followup.Resolver`，并放在模型 followup 前作为强规则兜底。
- “继续上一轮 / 继续刚才 / 接着刚才 / 展开 / 按刚才”等明确依赖当前活动任务的输入会优先绑定 active task，不再因为模型低置信直接进入 ambiguous。
- “昨天 / 上次 / 之前 / 历史”等可能指向非当前任务的输入仍交给模型结合历史候选判断，避免把历史续接误绑到 active task。
- 已新增回归测试 `TestRuntimeRuleFollowupBeatsLowConfidenceModelAmbiguity`。

### 现象

在连续会话批测里，用户已经明确说了“继续上一轮”，系统仍然经常返回“我还不能确定你是在继续哪个任务”，把任务打成待补充信息。

### 证据

- [`/Users/dongping/project/mateway/docs/测试文档.md`](/Users/dongping/project/mateway/docs/测试文档.md)
- [`/Users/dongping/.mateway/testdata/2026-05-20/125800-1.md`](/Users/dongping/.mateway/testdata/2026-05-20/125800-1.md)
- [`/Users/dongping/.mateway/testdata/2026-05-20/125836-2.md`](/Users/dongping/.mateway/testdata/2026-05-20/125836-2.md)
- [`/Users/dongping/project/mateway/internal/runtime/task_binding.go`](/Users/dongping/project/mateway/internal/runtime/task_binding.go#L258)
- [`/Users/dongping/project/mateway/internal/model/planner.go`](/Users/dongping/project/mateway/internal/model/planner.go#L206)

### 影响

- 连续会话体验断裂
- 用户明明是在接着上一轮说，系统却反复要求补一句上下文
- `followup` 的核心价值被削弱，尤其是在批量测试和真实对话里很明显

### 可能原因

- `resolveModelFollowup()` 完全依赖模型置信度阈值，低于阈值就直接转成 `ambiguous`
- 提示词里虽然给了 open/history 候选，但对“上一轮任务”没有足够强的确定性兜底
- `continue` 这类明确意图在模型侧仍然可能被判成不明确

## 2. 现有 `internal/followup` 规则没有真正进入主链

状态：已处理（2026-05-20）

处理记录：

- `AgentLoop.resolveTaskBinding()` 当前顺序为：approval reply -> slot fill -> rule followup -> model followup -> new task。
- `internal/followup/resolver.go` 不再只是孤立测试能力，已成为主链 task binding 的本地规则入口。
- 模型 followup 仍保留用于历史任务选择、open task 选择和复杂澄清。

### 现象

仓库里存在一个较完整的 `internal/followup/resolver.go`，里面已经能识别“继续”“重试”“展开”“按刚才这个文件”等 followup 意图，但运行时主链实际走的是 `resolveModelFollowup()`，没有直接消费这个规则化 resolver。

### 证据

- [`/Users/dongping/project/mateway/internal/followup/resolver.go`](/Users/dongping/project/mateway/internal/followup/resolver.go)
- [`/Users/dongping/project/mateway/internal/runtime/task_binding.go`](/Users/dongping/project/mateway/internal/runtime/task_binding.go#L258)
- [`/Users/dongping/project/mateway/internal/runtime/agent_loop.go`](/Users/dongping/project/mateway/internal/runtime/agent_loop.go)

### 影响

- 规则化 followup 逻辑和真实运行路径分裂
- 测试里能看到 heuristics 已存在，但线上行为仍然主要受模型输出和置信度影响
- 这会让“为什么明明是继续上一轮却还是 ambiguous”更难排查

### 可能原因

- followup 包被保留成了独立能力，但还没真正接到 `AgentLoop` 的任务绑定入口
- 当前 runtime 更信任模型 followup，而不是本地规则兜底

## 3. 测试报告里的 trace_id 和真实 runtime trace 不完全对齐

状态：已处理（2026-05-20）

处理记录：

- `runtime.Response` 已新增 `TraceID` 字段，由 `AgentLoop` 的真实请求级 trace id 填充。
- `mateway test` 报告不再使用 session key 派生 trace id，改为优先使用 runtime 返回的真实 `TraceID`。
- Trace Events 现在按真实请求级 trace id 读取，报告和 `~/.mateway/trace/events-YYYY-MM-DD.jsonl` 可以对齐排查。

### 现象

`mateway test` 生成的报告里，`trace_id` 使用的是测试命令自己拼出来的值，看起来更像 session key，而不是 runtime 实际写入 trace 的那个请求级 trace id。这会让报告里的 trace 片段和真实事件对不上，排障时容易误判。

### 证据

- [`/Users/dongping/project/mateway/cmd/mateway/main.go`](/Users/dongping/project/mateway/cmd/mateway/main.go)
- [`/Users/dongping/project/mateway/internal/runtime/runtime.go`](/Users/dongping/project/mateway/internal/runtime/runtime.go#L414)
- [`/Users/dongping/project/mateway/internal/observer/logger.go`](/Users/dongping/project/mateway/internal/observer/logger.go)
- [`/Users/dongping/project/mateway/testdata/2026-05-20/125929-1.md`](/Users/dongping/project/mateway/testdata/2026-05-20/125929-1.md)
- [`/Users/dongping/project/mateway/testdata/2026-05-20/130009-2.md`](/Users/dongping/project/mateway/testdata/2026-05-20/130009-2.md)

### 影响

- 报告里的 trace 可能找不到对应事件，或者把不同请求混到一起
- 用户看报告时会觉得“流程看起来完整，但 trace 对不上”
- 不利于把测试结果直接拿去做修复定位

### 可能原因

- 测试命令里为了方便按 session 汇总报告，自己生成了一个独立的 `trace_id`
- 这个 `trace_id` 没有和 runtime 实际的 message-level trace id 做统一

## 4. 批测任务的“通过”不等于答案质量真的足够

状态：已部分处理（2026-05-20）

处理记录：

- `mateway test` 报告新增“质量提示”段。
- 当任务机制完成但回复过短、缺少工具证据、分析型问题证据过少或回复偏工具痕迹时，结论会标为“任务机制已完成，但答案质量需要人工复核”。
- 这不是完整自动评分系统，只是先避免把“跑完”误读为“质量通过”。后续若需要可继续做独立质量评估器。

### 现象

部分测试任务虽然成功落了 Markdown 报告，但内容更像是“工具调用痕迹 + 形式化总结”，不一定真的完成了用户想要的分析深度。例如有些任务只做了文档重述、shell echo 或浅层总结，还是会被记录成已完成。

### 证据

- [`/Users/dongping/project/mateway/testdata/2026-05-20/125922-task.md`](/Users/dongping/project/mateway/testdata/2026-05-20/125922-task.md)
- [`/Users/dongping/project/mateway/testdata/2026-05-20/125940-task.md`](/Users/dongping/project/mateway/testdata/2026-05-20/125940-task.md)
- [`/Users/dongping/project/mateway/testdata/2026-05-20/125913-task.md`](/Users/dongping/project/mateway/testdata/2026-05-20/125913-task.md)

### 影响

- `mateway test` 能验证“机制通不通”，但还不能自动判断“答案够不够好”
- 批量回归容易出现“看上去跑完了，其实质量偏浅”的情况
- 如果后面要把测试结果交给人看，需要额外的质量检查标准

### 可能原因

- 目前测试命令只负责执行和落盘，没有独立的质量评分或失败判定
- 任务模板还没有强制区分“真正分析”与“只是调用工具后做形式总结”

## 结论

这轮批测里，最值得优先处理的是：

1. followup 续接稳定性
2. followup 规则和 runtime 主链的接线
3. 测试报告 trace 对齐

`HOME` 作为默认目录本身没有问题，这次主要问题不在默认根，而在 followup 绑定和测试报告可读性上。
