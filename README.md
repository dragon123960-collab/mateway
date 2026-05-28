# Mateway

Mateway is being rebuilt as a small Go agent runtime that keeps the useful outer shell from the previous project:

- CLI entry
- channel boundary
- config and `~/.mateway` layout
- gateway/session namespace
- a minimal transcript-driven agent core

The new runtime is intentionally small. The first milestone is a `pi`-style tool-call loop in Go, not a multi-agent router.

## Quick Start

```sh
go run ./cmd/mateway ask "hello"
go run ./cmd/mateway init
go test ./...
```

## Current Shape

- `internal/agentcore`: transcript-driven loop with `beforeToolCall`, `afterToolCall`, `prepareNextTurn`, `shouldStopAfterTurn`, steering, and follow-up hooks.
- `internal/config`: real `~/.mateway/config` loader and initializer.
- `internal/channel/feishu`: Feishu normalize/send/render boundary.
- `internal/gateway`: thin channel serving adapter.
- `scripts/`: local reset/restart helpers.
