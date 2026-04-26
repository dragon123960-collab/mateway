# Mateway vs OpenClaw 架构对比与改进方向

**创建时间**：2026-04-25  
**版本**：v1.0

---

## 概述

本文档对比 Mateway 与 OpenClaw 的核心架构差异，识别 Mateway 缺失的关键机制，并提出分阶段改进方向。

---

## 🔍 核心差距对比

| 维度 | OpenClaw | Mateway | 差距影响 |
|------|----------|---------|----------|
| **模型容错** | 自动 fallback 循环（429/超时/过载自动切模型） | 单模型调用，失败直接报错 | 高并发/限流时任务中断 |
| **上下文管理** | 自动压缩、静默回复过滤、消息分级保留 | 依赖 Eino 窗口，溢出直接失败 | 长对话/复杂任务容易崩溃 |
| **工具执行** | 异步结果链、流式推送、打字机指示器 | 同步阻塞执行，一次性返回 | 用户体验差，大任务无进度反馈 |
| **渠道抽象** | 统一 Channel Interface，消息去重，跨平台路由 | 仅飞书 WebSocket，无去重/路由 | 无法扩展多平台 |
| **错误恢复** | 错误分类器 + 指数退避重试 + 降级策略 | 记录日志，无重试/降级 | 网络波动/工具失效时直接失败 |
| **可观测性** | Token/成本追踪，结构化事件流，运行轨迹 | RunStep 记录，无成本/事件流 | 难以监控用量与优化成本 |

---

## 🛠️ Mateway 缺失的关键机制

### 1. 模型路由与 Fallback 机制

**现状**：`eino_runtime.go` 直接调用单一模型，失败抛错。

**缺失**：
- 模型健康检查
- 失败自动切换
- 冷却期管理
- 成本感知路由

**影响**：当主模型限流或不可用时，整个任务直接失败，无降级能力。

---

### 2. 自动上下文压缩 (Auto-Compaction)

**现状**：上下文窗口溢出时直接报错，无自动处理。

**缺失**：
- Token 用量监控
- 历史消息摘要生成
- 关键信息保留策略
- 静默消息过滤

**影响**：长对话或复杂任务中，上下文膨胀导致 API 调用失败。

---

### 3. 流式交付与进度反馈

**现状**：`harness.go` 同步执行，结果一次性返回。

**缺失**：
- SSE/WebSocket 流式推送
- 打字机信号
- 分块发送
- 媒体上传管道

**影响**：用户等待时间长，无进度感知，体验差。

---

### 4. 多渠道抽象与消息去重

**现状**：硬编码飞书 WebSocket 事件处理。

**缺失**：
- Channel 接口抽象
- 消息 ID 去重缓存
- 跨渠道会话绑定
- 回复 threading

**影响**：无法快速接入新渠道（微信、Telegram、Discord），重复消息无过滤。

---

### 5. 错误分类与分级重试

**现状**：`classifyTurnFailure` 仅记录，无自动恢复。

**缺失**：
- 错误分类器（限流/超时/上下文溢出/工具失效）
- 指数退避重试
- 降级策略（换工具/换搜索源/切简单模型）

**影响**：临时性故障（网络抖动、API 限流）导致任务永久失败。

---

### 6. 可观测性与成本追踪

**现状**：有 RunStep 记录，但无结构化指标。

**缺失**：
- Token 计数器
- 成本估算
- 事件发射器
- 运行轨迹导出

**影响**：无法追踪模型用量、优化成本、排查性能瓶颈。

---

## 🚀 改进方向（按优先级）

### P0：核心稳定性（1-2 周）

#### 1. 实现模型 Fallback 循环

```go
// 在 harness.go 或 eino_runtime.go 中
for _, model := range fallbackModels {
    result, err := callModel(model, prompt)
    if err == nil {
        return result
    }
    if isRateLimit(err) {
        wait(cooldown)
    }
    if isContextOverflow(err) {
        compact()
    }
}
```

**实现要点**：
- 配置多个模型（主模型 + 备用模型）
- 检测 429/过载/超时错误
- 指数退避等待
- 切换模型重试

#### 2. 上下文窗口监控 + 自动压缩

- 监控 token 使用量，接近阈值时触发摘要生成
- **保留**：系统提示、用户最新请求、工具调用结果
- **压缩**：历史对话、中间步骤、冗余上下文

```go
func (h *Harness) maybeCompactContext(run Run) error {
    if run.TokenUsage > h.Config.ContextWindow * 0.8 {
        // 生成摘要，保留关键信息
        summary := h.summarizeHistory(run.History)
        run.History = append([]Message{summary}, run.History[len(run.History)-5:]...)
    }
    return nil
}
```

#### 3. 错误分类器 + 重试策略

| 错误类型 | 策略 |
|----------|------|
| 429/过载 | 指数退避重试（1s → 2s → 4s → 8s） |
| 超时 | 切换备用模型/工具 |
| 上下文溢出 | 自动压缩后重试 |
| 工具失效 | 降级到 web_search 或 browser_fetch |

---

### P1：用户体验（2-3 周）

#### 4. 流式输出管道

- 实现 SSE 或 WebSocket 流式推送
- 打字机信号：`typingSignals.signalTextDelta()`
- 分块发送：大消息自动切分，避免渠道限制

```go
// 流式响应示例
func (h *Harness) streamResponse(run Run, w http.ResponseWriter) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "Streaming not supported", 500)
        return
    }
    w.Header().Set("Content-Type", "text/event-stream")
    
    for chunk := range run.Stream {
        fmt.Fprintf(w, "data: %s\n\n", chunk.Text)
        flusher.Flush()
    }
}
```

#### 5. 工具异步执行链

- 工具调用改为异步，结果通过 channel 返回
- 支持并行独立工具调用（如同时搜索+读文件）
- 工具结果流式注入 LLM 上下文

#### 6. 消息去重与会话绑定

- 基于 `message_id` + `session_key` 去重
- 飞书/微信/Telegram 事件统一映射到内部会话模型

---

### P2：生产级能力（3-4 周）

#### 7. 多渠道抽象层

```go
type Channel interface {
    Ingress() <-chan Message
    Egress() chan<- Reply
    Dedup(msgID string) bool
}
```

- 统一消息格式，解耦渠道实现
- 支持飞书、微信、Telegram、Discord、WhatsApp

#### 8. 可观测性体系

- **Token/成本追踪**：每次调用记录 prompt/completion tokens
- **事件流**：`emitAgentEvent(runID, "tool_start", toolName)`
- **指标导出**：Prometheus/OpenTelemetry 集成

#### 9. 安全与沙箱增强

- 工具调用超时控制
- 输出大小限制（防止大文件/长文本撑爆上下文）
- 敏感操作二次确认（exec/schedule/spawn）

---

## 📐 架构演进建议

### 当前架构

```
Mateway/
├── harness.go           # 同步执行
├── eino_runtime.go      # 单模型调用
├── tool_selection.go    # 工具选择
└── capabilities/        # 权限编译
```

### 演进目标

```
Mateway/
├── channel/             # 多渠道抽象 + 去重
├── runner/              # Fallback 循环 + 流式管道
├── context/             # 自动压缩 + 窗口监控
├── observability/       # Token/成本 + 事件流
└── harness.go           # 保留核心，接入新模块
```

**关键原则**：
- 不推翻现有架构，用**中间件/插件**方式补齐
- 优先实现 **Fallback 循环 + 上下文压缩**（解决 80% 线上故障）
- 流式输出和渠道抽象可并行开发，不影响核心逻辑

---

## 📊 速查表

| 机制 | OpenClaw | Mateway | 改进优先级 |
|------|----------|---------|------------|
| 模型 Fallback | ✅ 自动循环 | ❌ 单模型 | P0 |
| 上下文压缩 | ✅ 自动 | ❌ 无 | P0 |
| 错误分类重试 | ✅ 分级策略 | ❌ 仅记录 | P0 |
| 流式输出 | ✅ 打字机+分块 | ❌ 同步阻塞 | P1 |
| 工具异步链 | ✅ 并行+流式注入 | ❌ 同步 | P1 |
| 多渠道抽象 | ✅ 统一接口 | ❌ 硬编码飞书 | P2 |
| 消息去重 | ✅ 内置 | ❌ 无 | P2 |
| 成本追踪 | ✅ Token/用量 | ❌ 无 | P2 |
| 事件可观测 | ✅ 结构化流 | ⚠️ RunStep | P2 |

---

## 📝 实施计划

### 第一阶段：核心稳定性（Week 1-2）

- [ ] 实现模型 Fallback 循环
- [ ] 上下文窗口监控 + 自动压缩
- [ ] 错误分类器 + 重试策略
- [ ] 工具调用超时控制

### 第二阶段：用户体验（Week 3-4）

- [ ] 流式输出管道（SSE）
- [ ] 工具异步执行链
- [ ] 消息去重与会话绑定
- [ ] 打字机信号

### 第三阶段：生产级能力（Week 5-6）

- [ ] 多渠道抽象层
- [ ] Token/成本追踪
- [ ] 事件流 + 指标导出
- [ ] 安全沙箱增强

---

*文档维护：大强 | 最后更新：2026-04-25*
