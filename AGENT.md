# Mateway Development Guide

这份文档给后续在本仓库工作的 agent / 开发线程使用。

目标只有三个：

1. 保持主运行链稳定
2. 让新能力以宿主协议方式长出来
3. 避免重新走回“大而全内核”

---

## 1. 产品边界

`Mateway` 当前不是：

- 一个大而全聊天前端
- 一个先做复杂 DAG 的 workflow 平台
- 一个把业务逻辑全塞进内核的 monolith

`Mateway` 当前是：

- 一个消息驱动个人助理宿主
- 一个可接入 CLI / API / skill 的 agent runtime
- 一个以后可以长出 workflow / multi-agent 的底座

做设计时优先问自己：

- 这个能力应不应该放进宿主？
- 还是应该放进 `skill / config / workspace` 层？

默认优先后者。

---

## 2. 当前主链

后续改动必须优先保护这条链：

1. `mateway init`
2. 写 `~/.mateway/config/`
3. 写 `~/.mateway/workspace/*.md`
4. `mateway doctor`
5. `mateway gateway`
6. 飞书 websocket 收到消息
7. 走 prompt 装配 + LLM
8. 回复用户

任何新改动，如果会破坏这条链，优先回退复杂性。

---

## 3. 架构原则

### 薄宿主

- 宿主负责运行时、配置、路由、诊断、观察性
- 业务能力尽量通过 skill / API / workflow 外置

### 配置分层

- 主配置：`~/.mateway/config/config.yaml`
- 模型：`~/.mateway/config/models/*.yaml`
- 通道：`~/.mateway/config/channels/*.yaml`
- workspace 指令层：`~/.mateway/workspace/*.md`

不要把大量可拆分配置重新塞回一个大 YAML。

### 可热更新

- `skills/` 目录变更后应尽量自动生效
- prompt markdown 的变化也应尽量下轮生效
- 不要默认要求用户重启 gateway

### 跨平台优先

- 核心运行时避免写死 macOS 语义
- `launchd`、`systemd`、Windows service 都应视为适配层

---

## 4. 编码约束

- 核心语言保持 Go
- 新增 Go 文件后运行 `gofmt`
- 改动后至少跑受影响测试，默认优先 `go test ./...`
- 新增公共行为要有测试，至少覆盖：
  - 配置加载
  - channel 行为
  - LLM 请求装配
  - workspace / prompt 装配

避免：

- 没有测试的协议改动
- 大段未使用代码
- 为未来想象过度设计接口

---

## 5. Prompt 与聊天规则

当前 prompt 由这几层装配：

- `models.system_prompt`
- `workspace/SOUL.md`
- `workspace/AGENT.md`
- `workspace/USER.md`

改这块时注意：

- prompt 装配必须可读、可解释
- 不要把 prompt 逻辑深埋在 channel 内
- channel 只负责收发，不负责主 prompt 策略

---

## 6. Feishu 约束

当前飞书路线：

- websocket 常驻
- group 默认 `mention_only`
- allowlist 优先在入口就过滤

后续如果扩能力，优先顺序：

1. 稳定 direct / group 行为
2. 稳定 reconnect / error logging
3. 再补 reaction / placeholder / richer message types

不要一开始把图片、文件、卡片、音频全部做重。

---

## 7. Skill 约束

skill 当前默认是：

- 目录发现
- `skill.yaml`
- 外部执行

后续做 skill 时：

- 优先保证 manifest 稳定
- 优先保证失败时有清晰日志和 reflection
- 优先保证对宿主低耦合

不要优先做进程内插件系统。

---

## 8. 提交前检查

在一个开发线程结束前，至少确认：

- [ ] `go test ./...` 通过
- [ ] 主链未破坏
- [ ] README / TODO / AGENT.md 至少一处同步
- [ ] 运行方式仍清晰
- [ ] 新配置项已经落到分目录配置体系

如果改了运行链，额外确认：

- [ ] `mateway doctor`
- [ ] `mateway gateway`
- [ ] 飞书或本地健康检查

