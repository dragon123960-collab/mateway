# Mateway 开发说明

这份文档只保留后续开发最需要记住的内容。

## 1. 先看哪两份文档

新开对话或新接手开发时，优先看：

1. `docs/项目运行机制.md`
2. `docs/飞书测试与验收.md`

这两份文档现在是项目的最小有效说明集。

## 2. 当前项目主线

Mateway 现在的核心不是“聊天 UI”，而是：

- 渠道入口
- Harness 统一执行链
- 任务主档、结果、记忆、失败经验沉淀

主运行时是 Eino，主代码入口在：

- `internal/harness/harness.go`
- `internal/harness/eino_runtime.go`
- `internal/harness/eino_middleware.go`
- `internal/channels/feishu/handler.go`
- `internal/memory/store.go`
- `internal/memory/task_records.go`

## 3. 当前必须保护的链路

任何改动都优先保护这条链：

1. 飞书或本地入口收到消息
2. 任务被分类为 `task_type`
3. Harness 创建 `Run` 和 `TaskRecord`
4. 选择 route、skill、tool
5. 执行并记录 `RunStep / RunEvent`
6. 生成 `CompletionContract`
7. 写回：
   - `memory/tasks/`
   - `memory/runs/`
   - `memory/summaries/`
   - `memory/lessons/`
   - 必要时 `memory/wiki/`

## 4. 现在默认遵守的原则

- `task_id` 是任务真相主键，`run_id` 是执行轨迹主键
- `TaskRecord` 是结果主入口，不再只靠 `Run.Result`
- `wiki` 只做精选知识库，不做任务流水账
- 失败要进入结构化 `lesson`
- schedule 查询优先看 `schedule state/history + task record`

## 5. 开发约束

- 代码用 Go
- 改完默认跑 `go test ./...`
- 新增 Go 文件后运行 `gofmt`
- 新行为优先写测试，再补文档
- 不要把业务能力重新塞回内核，优先放到 task record、lesson、channel adapter、skill 层

## 6. 最小验证流程

```bash
go test ./...
GOCACHE=/tmp/mateway-gocache go build -o build/mateway ./cmd/mateway
./scripts/restart-launchd-service.sh
curl -sS http://127.0.0.1:8787/health
```

如果要从零验证新记忆体系：

```bash
mateway memory rebuild --force --drop-all
```

## 7. 当前最值得继续补强的点

- `CompletionContract` 约束继续收紧
- `TaskRecord` 检索和展示继续统一
- `memory/lessons/` 的命中与复用继续增强
- schedule 任务的成果回查继续做厚
- 精选 wiki 的判定规则继续收紧
