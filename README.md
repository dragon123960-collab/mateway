# Mateway

Mateway 是一个面向 **个人助理、飞书机器人、任务编排、工具调用、定时任务、长期记忆沉淀** 的 Agent 宿主。

它的重点不是“多一个聊天机器人”，而是让一条任务从进入、执行、交付、复盘到记忆沉淀都可追踪、可复用、可持续演进。

## 项目优势

Mateway 现在最有价值的地方有 5 个：

1. **任务、记忆、结果一体化**  
   顶层任务会进入统一执行链，并沉淀为任务主档、运行轨迹、产出物索引和失败经验。

2. **飞书接入直接可用**  
   既可以做飞书里的日常助理，也可以承接定时提醒、问答、排查、调研类任务。

3. **不只会答，还会留下证据**  
   运行结果、失败原因、学习报告、结构化日志都有固定落点，不容易出现“做完了但成果在哪”的黑盒问题。

4. **适合接本地工具和外部能力**  
   支持内置工具、skills、外接 CLI provider，后续也适合继续接更多渠道和能力。

5. **适合长期演进**  
   不是一次性脚本，而是可以持续长出任务分类、恢复策略、失败经验和精选知识库的宿主。

## 可以用来做什么

Mateway 适合这些场景：

- 做一个飞书里的个人助理或团队助理
- 做每天/每周定时提醒、定时整理、定时巡检
- 做本机 CLI 学习与半自动操作入口
- 做调研、排查、总结、复盘类任务的执行宿主
- 做带长期记忆和失败经验沉淀的 Agent Runtime

## 安装方式

当前推荐两种方式：

### 1. 从源码安装

环境要求：

- Go 1.25+
- Git

在项目根目录执行：

```bash
go build -o build/mateway ./cmd/mateway
```

验证安装：

```bash
./build/mateway version
./build/mateway help
```

### 2. 使用预编译二进制

如果你拿到的是已经编译好的二进制，只需要把它放到 PATH 中的目录即可。

例如 macOS / Linux：

```bash
chmod +x mateway
mv mateway /usr/local/bin/mateway
```

验证安装：

```bash
mateway version
mateway help
```

## 快速开始

### 1. 初始化目录

第一次运行：

```bash
mateway init
```

它会在默认位置创建：

```text
~/.mateway/
├── config/
└── workspace/
```

### 2. 配置模型

主配置在：

```text
~/.mateway/config/config.yaml
```

模型分片在：

```text
~/.mateway/config/models/
```

你至少需要配好一个可用模型。

### 3. 启动 gateway

前台运行：

```bash
mateway gateway start
```

检查健康状态：

```bash
mateway gateway health
```

如果返回：

```text
ok
```

说明 gateway 正常。

### 4. 本地使用

打开本地交互终端：

```bash
mateway tui
```

常用命令：

```text
/skills
/tools
/runs
/summary
/last
/trace
/learn
```

### 5. 飞书使用

如果你已经配置好飞书通道，直接在飞书里对话即可。

推荐先用这些内容验证：

```text
你好
请调研北京 AI 活动并整理结论
你看看 zsh 下 lark-cli 怎么用
/schedule list
```

更完整的测试话术和结果落点，请看：

- [docs/项目运行机制.md](</Users/dongping/project/mateway/docs/项目运行机制.md>)
- [docs/飞书测试与验收.md](</Users/dongping/project/mateway/docs/飞书测试与验收.md>)

## 常用命令

```bash
mateway help
mateway doctor
mateway gateway start
mateway gateway health
mateway gateway status
mateway logs show
mateway logs structured --json
mateway schedule list
mateway schedule get <name>
mateway schedule runs <name>
mateway run list
mateway run get <run-id>
```

如果你要从零重建新的记忆体系：

```bash
mateway memory rebuild --force --drop-all
```

注意：这条命令会清空当前 workspace 下的全部 `memory/` 数据。

## 项目现在的运行特点

Mateway 当前主线已经具备：

- Eino 驱动的统一 chat / tool / approval / schedule 执行链
- `task_type` 任务分类
- `TaskRecord` 任务主档
- `CompletionContract` 结果收口
- `RunEvent / RunStep` 运行轨迹
- `memory/lessons/` 失败经验沉淀
- `memory/learning/reports/` 正式复盘报告
- `memory/wiki/` 精选长期知识库

如果你想一次性看懂这些数据分别写到哪里，请直接看：

- [docs/项目运行机制.md](</Users/dongping/project/mateway/docs/项目运行机制.md>)
- [docs/飞书测试与验收.md](</Users/dongping/project/mateway/docs/飞书测试与验收.md>)

## 开源协议与商用说明

### 使用说明

除第三方依赖外，**本仓库代码按当前项目作者公开说明开放使用**：

- 允许个人、企业、组织用于商用
- 允许二次开发、部署、改造
- 允许在内部系统或对外产品中集成使用

前提是：

- **请注明来源为本仓库**
- 建议在文档、关于页、仓库说明、发行说明或代码注释中保留项目名称与仓库地址

推荐注明方式：

```text
本产品/项目基于 Mateway 修改或集成开发：
https://github.com/dongping/mateway
```

### 额外说明

- 第三方依赖仍然分别遵循它们各自的开源协议
- 如果后续仓库根目录新增正式 `LICENSE` 文件，则**以 LICENSE 文件为准**

## 为什么值得用

如果你要的只是一个简单机器人，Mateway 可能偏重。  
但如果你要的是下面这些能力的组合，它就很合适：

- 飞书对话入口
- 可执行任务而不只是闲聊
- 有结构化结果和产出物定位
- 失败后有 lesson 和复盘沉淀
- 适合继续长出更多 skills、CLI、定时任务和长期知识

一句话概括：

**Mateway 更像一个“能长期成长的助理宿主”，而不只是一个会回复消息的机器人。**
