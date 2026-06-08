# Agent Observability

Mateway should expose the agent loop as channel-neutral process events, then let each channel render those events in the style that fits the surface.

## Event Model

The runtime should emit a stable stream of process events:

- `model.thinking`: the model call is in progress or has produced an assistant turn.
- `tool.started`: a tool call is about to run, including tool name and compact arguments.
- `tool.progress`: a long-running tool is still active, including elapsed time.
- `tool.completed`: a tool returned, including status, duration, compact summary, and optional `raw_ref`.
- `tool.blocked`: policy blocked a tool call.
- `approval.requested`: a guarded or dangerous operation needs user confirmation.
- `final.completed`: final answer is ready.
- `final.failed`: the task stopped at a safe point.

These events are not chain-of-thought. They describe operational state: what is being called, what returned, what is waiting, and what requires approval.

## CLI Renderer

The CLI is Mateway's local control center, developer debugger, and Unix pipe endpoint.

Default interactive mode should render the full loop:

```text
user> Check tomorrow's Beijing weather and remind me if I need an umbrella
[thinking] Waiting for model output...
[tool] weather.get {"city":"Beijing","date":"tomorrow"}
[result] rain, 15C
[thinking] Prepared final answer
[final] Tomorrow in Beijing is rainy and 15C. Remember your umbrella.
```

Requirements:

- Stream model output and process events as they arrive.
- Do not render final answer text inside progress events; final content belongs in the final renderer.
- Use color and symbols for status in TTY mode.
- Collapse long tool results by default; expose details through verbose mode, `raw_ref`, or an explicit expand command.
- Support `--quiet` for final-answer-only scripting.
- Support `--json` / NDJSON for machine-readable automation.
- Support stdin and stdout piping, for example `cat error.log | mateway ask "analyze this"`.
- Add approval prompts for guarded and dangerous operations.
- Allow interruption and correction with Ctrl+C without losing the current task context.

## Feishu Renderer

Feishu process rendering should prioritize tool activity:

- Show every tool call with compact command/path/query/url arguments.
- Show every tool result as success, failed, blocked, or timed out.
- Keep model waiting/progress as lightweight context only.
- Keep recent steps visible and collapse verbose details.

If Feishu message update rejects a message type, fall back to a text progress message plus a final card, rather than hiding process events.

## Weixin Renderer

Weixin should be conservative because message update and rich interactions are limited.

- Avoid sending every event as a separate message by default.
- Send key milestones for long-running tasks.
- Include a compact process summary in the final reply.
- Consider an H5 details page later for full live traces.

## Implementation Path

P0:

- Keep emitting runtime process events from `agentcore.Hooks`.
- Show `model.thinking`, `tool.started`, `tool.progress`, and `tool.completed` in Feishu progress text/card.
- Add CLI `ask` mode with readable ReAct-style output.

P1:

- Emit NDJSON process events from CLI.
- Make `/trace` and `/events` show detailed tool call arguments, result outcomes, duration, and compact result summaries.
- Prevent final answer text from being edited into Feishu progress messages.

P2:

- Actively fetch history from Feishu/Weixin APIs when local session context is not enough.
- Add `mateway send --to ...` for cross-channel messages.
- Add `/model` and tool enable/disable management.
- Add richer TTY color, folding, and expansion.
