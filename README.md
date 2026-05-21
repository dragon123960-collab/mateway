# Mateway

Mateway is a small Go agent runtime for connecting LLMs to real tools, local workspaces, and business systems.

The current focus is deliberately practical:

- run from a single binary
- work from CLI and Feishu
- call safe local tools with confirmation boundaries
- keep configuration and skills editable under `~/.mateway`
- let teams add API/CLI capabilities without rebuilding the core runtime

Mateway is not trying to become a heavyweight workflow engine. The core loop stays small:

```text
receive -> plan -> policy -> act -> observe -> synthesize -> reply
```

## Status

Mateway is under active development. The single-agent runtime is usable, and the next major phase is memory plus enterprise capability integration.

Implemented:

- CLI `ask`, `doctor`, `test`, and `trace`
- Feishu WebSocket receive/reply/reaction
- Anthropic-compatible and OpenAI-compatible model clients
- model/agent configuration with explicit defaults and fallback metadata
- file read/write/patch tools with path guards and confirmation
- shell command tool with dangerous-command confirmation
- web search, time, config summary, project index, and file summary tools
- session/task state, follow-up resolution, trace events, and response sanitization
- workspace skills loaded from `~/.mateway/workspace`
- binary-first `mateway init` that creates config, samples, docs, and default skills

Not yet done:

- long/short memory system
- self-learning and durable knowledge curation
- multi-agent runtime routing beyond the configuration contract
- enterprise API/CLI connector packaging
- optional structured workflow mode

## Binary Quick Start

Download or build one `mateway` binary, then initialize local runtime files:

```bash
mateway init
```

This creates `~/.mateway` and writes configuration, sample files, docs, workspace skills, and the default agent context. Existing real config files are not overwritten.

Important generated files:

```text
~/.mateway/config/
  README.md
  config.yaml
  config.sample.yaml
  mateway.env.sample
  models/
    minimax.yaml
    minimax.sample.yaml
    local-mlx.yaml
    local-mlx.sample.yaml
  channels/
    feishu.yaml
    feishu.sample.yaml
```

Then configure secrets and validate:

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
mateway doctor
```

## Build From Source

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway
go test ./...
go build -o build/mateway ./cmd/mateway
./build/mateway init
./build/mateway doctor
```

## Common Commands

```bash
mateway init
mateway doctor
mateway ask "What time is it?"
mateway ask "Run pwd, then read README.md and summarize it."
mateway gateway serve
mateway gateway status
mateway gateway restart
mateway trace tail
mateway trace show <trace_id>
```

## Configuration Model

Runtime config lives under `~/.mateway/config`.

- `config.yaml` controls app paths, security, search, model defaults, and agent profiles.
- `models/*.yaml` declares model endpoints and API compatibility.
- `channels/feishu.yaml` configures Feishu.
- `mateway.env` stores local secrets and should not be committed.
- `*.sample.yaml` files are user templates and are ignored by the runtime loader.

Model config currently supports:

- `api: anthropic`
- `api: openai`

Agent model config can declare:

```yaml
model:
  default: minimax
  fallbacks: []
  roles:
    planning: minimax
    repair: minimax
    synthesis: minimax
    followup: minimax
```

Today the runtime uses the default model. Role-specific model routing and real fallback retries are planned.

## Security Boundaries

Mateway is designed to make tool use explicit and observable.

- File tools are restricted to the current project root, Mateway workspace, and configured `accessible_paths`.
- File write/patch tools require confirmation.
- Dangerous shell commands require confirmation.
- Feishu replies are sanitized to avoid leaking raw tool-call traces.
- Runtime events are written to trace logs for debugging.

This is still early software. Review configuration and confirmation boundaries before using it on sensitive machines.

## Skills And Enterprise Connectors

Mateway’s extension direction is skill-first:

```text
skill = instructions + metadata + optional scripts/assets + allowed tools
```

The next intended enterprise use case is:

1. A traditional system exposes existing APIs, CLIs, or scripts.
2. The team describes them in a Mateway skill/connector package.
3. Mateway validates arguments, risk level, and confirmation boundaries.
4. The agent calls the API/CLI through tools or fixed mini-workflows.
5. Results are summarized back to the user through CLI, Feishu, or future channels.

This keeps legacy integration close to the business system while avoiding a giant central workflow engine.

## Memory Direction

The planned memory system is Markdown-first:

- short memory: recent session/task state
- long memory: curated Markdown notes under the agent workspace
- evidence and provenance recorded with each durable note
- optional SQLite index for metadata/search when Markdown alone is not enough

The project does not need a heavy “LLM wiki” or vector-RAG stack as the first step. A transparent Markdown knowledge base plus a small index is easier to inspect, edit, back up, and trust.

## Repository Layout

```text
cmd/mateway              CLI entrypoint
internal/config          config loading and init templates
internal/model           model clients and plan normalization
internal/runtime         agent loop, task binding, session flow
internal/tool            built-in tools and safety policy
internal/skill           skill discovery and default skill templates
internal/channel/feishu  Feishu channel adapter
internal/gateway         channel orchestration and service management
internal/observer        trace and event inspection
docs/                    development notes and internal planning
```

Documents under `docs/` are development notes. The root README is the public project entry point.

## License

This project is licensed under the Apache License 2.0.

Apache-2.0 is permissive and includes an explicit patent grant, which makes it a reasonable default for an independent developer who wants open-source adoption and future enterprise use. Commercial support, hosted services, private connectors, or dual-licensed enterprise modules can still be offered separately.
