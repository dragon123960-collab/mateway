# Runtime 执行流程

Mateway 当前主线是一个小型 transcript-driven agent runtime，参考 Pi-style agent loop：runtime 不做语义路由，不做多 agent supervisor，也不把任务完成判断外包给额外 review 模型。

## 主流程

```text
channel -> gateway -> runtime setup -> AgentCore loop -> tools -> observe/finalize
```

Runtime 处理一条用户消息时的优先级：

1. `/new`：归档并清空当前 active state。
2. `memory_proposal_review` pending：只接受数字选择。
3. 有 open active task：用户新消息直接 steering 到这个任务。
4. 无 active task：创建新 task，并注入 previous task weak context，让模型自己判断是否接续。
5. AgentCore loop：模型调用工具则执行并继续；模型不再调用工具则停止回复；达到 `max_iterations` 或发生错误则停止并保留任务状态。

## Open Task 状态

以下状态会被视为 open，下一条用户消息会直接进入同一个任务：

- `running`
- `await_user_input`
- `failed`
- `resuming`

`completed` 任务不会被 runtime 自动重新激活。它们只会作为 weak previous context 提供给模型参考，或者由 agent 使用 `task.search` / `task.resume` 明确找回。

## 下一句话接续判断

当没有 active task 时，runtime 会在 system prompt 的 `Continuity judgment` 段落中放入最近任务上下文。这不是 followup router，也不会自动恢复已完成任务；它只是给模型一个弱参考。

模型应根据当前用户消息、transcript 和 previous task context 自己判断：

- 如果当前消息很短、缺少明确对象，或像是在回应上一个任务的阻塞条件，可以参考 previous context 继续。
- 如果当前消息明显是一个独立新目标，就忽略 previous context。
- 如果用户明确要找回旧任务，agent 应使用 `task.search`，必要时再 `task.resume`。

Runtime 不再通过中英文短语 alias、followup model 或独立 router 来判断下一句话。

## 停止与状态

- 模型给出实质性 final answer 且没有工具调用：任务完成，清空 active task。
- 模型请求用户补充信息：任务进入 `await_user_input`，继续保留 active task。
- 达到 `max_iterations`、模型错误或 activity timeout：任务标记为 `failed` 或 partial，继续保留 active task。用户说“继续”时会 steering 回原任务。
- 模型说“我来检查/我来执行”但没有工具调用：视为空承诺，不算完成，任务保持 open。

## Hooks

- `context_hook`：注入 runtime context、workspace prompt、memory safe-read。
- `tool_policy_hook`：只做硬边界，例如 destructive terminal block、路径限制、secret 边界。
- `observe_hook`：记录 trace、tool evidence、diary、memory proposal、learning evidence。
- `response_hook`：secret redaction、回复清理、memory proposal 数字选择提示。

这些 hook 不能判断任务完成，不能审批，也不能路由用户消息。
