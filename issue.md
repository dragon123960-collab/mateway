# Mateway 当前问题清单

更新时间：2026-05-20

历史完整记录已归档到：

- [`docs/archive/issue-20260520-141704.md`](/Users/dongping/project/mateway/docs/archive/issue-20260520-141704.md)

## 当前未闭环

### 1. 最新信息来源质量仍只是软约束

状态：已部分处理，待继续增强

现象：

- “2026 最新 AI 课程 / 趋势”类任务能跑完，但仍可能混入二手榜单、转载页、时间戳不明页面。

后续方向：

- 在工具层返回结构化来源分类与排序。
- 或新增 source gate：二手/时间不明来源不能单独支撑“最新/官方/最热”结论。

### 2. 测试报告质量判断仍是轻量提示

状态：已部分处理，非阻塞

现象：

- `mateway test` 能提示“机制完成但质量需复核”，但还不是独立自动评分系统。

后续方向：

- 如需要自动回归质量，可新增独立 quality evaluator。
- 当前阶段先人工复核报告即可。

### 3. 飞书模型服务瞬断需要更稳的重试策略

状态：已包装用户提示，待后续增强

现象：

- 飞书里出现过 `unexpected EOF`，说明 MiniMax/网络偶发失败会中断本轮请求。

已处理：

- 底层 URL / HTTP / `unexpected EOF` 不再直接暴露给用户。
- trace 里会保留 `error_detail` 方便排障。

后续方向：

- 在 model client 层增加有限重试和退避。
- 区分可重试错误与不可重试错误。

### 4. 飞书 MiniMax tool_call 标签回显

状态：已处理，待飞书实机回归

现象：

- 飞书最终回复曾直接出现 `<minimax:tool_call>`。
- 内容包含 `file.read args / risk / requires_confirm` 等内部工具调用字段。

已处理：

- runtime sanitizer 已清理 `<minimax:tool_call>...</minimax:tool_call>` 形态。
- 飞书出站渲染增加二次兜底，会过滤 MiniMax tool_call 标签和工具调用详情行。
- 已补单测覆盖连续两个 `file.read` 工具调用标签泄漏的截图场景。

下一步验收：

- 飞书实机再发“总结测试目标”类任务。
- 通过标准：不出现 `<minimax:tool_call>`、`file.read args`、`risk`、`requires_confirm` 等内部字段。

### 5. 项目/测试总结可能缺少真实文档证据

状态：已处理，待飞书实机回归

现象：

- 用户要求“总结当前 Mateway 的测试目标”时，系统会说“让我查看测试文档获取具体目标”。
- 但如果没有真实 `file.read / file.summary / project.index` 结果支撑，最终容易变成泛化回答，像是在敷衍。

已处理：

- runtime 增加 grounded evidence gate：当前项目、仓库、测试目标等总结类请求必须有文件或项目索引证据。
- 第一版 plan 没拿到证据时，会触发 plan repair，要求补足 `file.read / file.summary / project.index`。
- repair 后仍无证据时，会停止生成泛化结论，返回用户可读失败提示。
- 已补单测覆盖“先只查时间，repair 后读测试文档”和“repair 后仍无文档证据则阻断泛化回答”。

下一步验收：

- 飞书实机再发“请总结当前 Mateway 的测试目标，控制在两句话”。
- 通过标准：回答应基于真实文档/项目证据；如果没读到证据，应明确失败，不应口头承诺“我去看文档”后泛化回答。

### 6. 多步 web.search 计划 JSON 缺少 step 闭合括号

状态：已处理，待飞书实机回归

现象：

- 飞书趋势分析任务在 plan 阶段失败，工具没有开始执行。
- trace 显示 MiniMax 返回的计划 JSON 中，多个 `web.search` step 少了外层 `}`，形如 `args:{...},{id:"step-2"...}`。

已处理：

- `parsePlan()` 增加 step 对象闭合括号修复兜底。
- 修复器会按 `steps` 数组里的 step 边界切分，并根据花括号平衡度补齐缺失的 `}`。
- 已补单测覆盖截图中的 AI 趋势多步搜索计划。
- 已抽出 `PlanChecker / PlanNormalizer` 第一版，统一负责 raw plan JSON 的提取、修复、schema 校验和默认字段补齐。
- runtime trace 会记录 `checker_fixed / checker_warns`，后续能直接看到计划是否被自动修过。

下一步验收：

- 飞书实机重发“搜集现在的 ai 趋势...”任务。
- 通过标准：不再在 plan 阶段因 JSON 少括号失败，应进入 `web.search` 工具执行。
