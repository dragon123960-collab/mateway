# Mateway TODO

## 当前状态

已经落下来的主干：

- Eino 是默认 runtime
- Feishu 与本地 `tui` 可用
- `task_type` 已接入 `Request / Run / trace / summary`
- `TaskRecord / CompletionContract / LessonRecord` 已接入主链
- schedule 执行会写 `last_task_id`
- `memory rebuild --force --drop-all` 已实现
- 学习报告写到 `memory/learning/reports/`

## 当前最重要的待办

### P0

- 收紧 `CompletionContract` 生成规则  
  让 `schedule_task / code_write / diagnose_task` 在没有明确 `primary_artifact` 时稳定降级为 `partial`

- 统一 `TaskRecord` 展示入口  
  继续收口 `/last /summary /trace /schedule`，尽量都先看 task record

- 真实切换到新记忆体系前，执行一次  
  `mateway memory rebuild --force --drop-all`  
  并做人工验收

### P1

- 优化 `memory/lessons/` 命中质量  
  让相似任务更稳定命中 lesson，而不是只靠关键词碰运气

- 收紧 wiki 生成策略  
  failure lesson 只在长期复用价值明显时再写入 wiki

- 提升 artifact 推断质量  
  尤其是 schedule、diagnose、code_write 三类任务

### P2

- 为 `TaskRecord` 增加更强的查询能力  
  按 session / task_type / schedule_name / status 做快速检索

- 增强 structured logs 与 diagnostics  
  让“为什么失败”更快从 run/log/task/lesson 串起来

## 每次改完默认检查

```bash
go test ./...
GOCACHE=/tmp/mateway-gocache go build -o build/mateway ./cmd/mateway
./scripts/restart-launchd-service.sh
curl -sS http://127.0.0.1:8787/health
```

如果要验证全新记忆体系：

```bash
mateway memory rebuild --force --drop-all
```
