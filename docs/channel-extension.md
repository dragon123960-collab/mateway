# Channel Extension Protocol

This document explains how new channels should connect to Mateway. The preferred product shape is built-in channel support: `mateway gateway serve` starts all enabled channels from `~/.mateway/config/channels/`. For fast second-party development, prototypes, or channels with heavy platform dependencies, Mateway also keeps a small bridge protocol.

## Model

Mateway owns the runtime path:

```text
built-in channel -> Mateway gateway -> runtime -> AgentCore -> reply
```

Built-in channels own platform-specific details:

- login, QR code, OAuth, SDK setup, webhook verification
- platform message IDs and delivery retries
- normalizing platform messages to `channel.InboundMessage`
- sending `channel.OutboundMessage` back to the platform

The bridge protocol mirrors the same boundary over HTTP. It is useful when a channel should live outside the Go binary temporarily, but stable platform channels should eventually become built-in channel specs so one gateway process can manage them.

Mateway v1 only processes text through the bridge protocol. Channels may include attachment metadata for traceability, but the current runtime will not download or reason over the media yet.

## Adding A Built-In Channel

New built-in channels should follow the existing Feishu shape:

1. Add a package under `internal/channel/<name>/`.
2. Normalize inbound platform events into `channel.InboundMessage`.
3. Send replies from `channel.OutboundMessage`.
4. Add `<Name>Config` under `config.ChannelsConfig`.
5. Add a YAML template under `~/.mateway/config/channels/<name>.yaml`.
6. Register a `channelSpec` in `internal/gateway/gateway.go`.

The channel should not call runtime directly. It should only hand normalized messages to gateway helpers and let gateway handle session key, dedupe, async execution, and trace events.

Keep channel code focused on I/O and message normalization. Generic planning, tool policy, and AgentCore behavior belong in runtime, not in channel packages.

## Enable Bridge Protocol

Edit `~/.mateway/config/channels/bridge.yaml`:

```yaml
bridge:
  enabled: true
  addr: 127.0.0.1:8789
  base_path: /channels
  token: ""
  token_env: MATEWAY_BRIDGE_TOKEN
  allowed_channels: []
```

Put the token in `~/.mateway/config/mateway.env`:

```bash
MATEWAY_BRIDGE_TOKEN=replace-with-a-random-token
```

Then start the gateway:

```bash
mateway gateway serve
```

## Send Events To Mateway

External bridge integrations post normalized events to:

```text
POST http://127.0.0.1:8789/channels/{channel}/events
Authorization: Bearer <MATEWAY_BRIDGE_TOKEN>
Content-Type: application/json
```

Example:

```json
{
  "id": "msg-001",
  "channel": "dingtalk",
  "account_id": "bot-main",
  "peer_id": "chat-123",
  "thread_id": "chat-123",
  "user_id": "user-456",
  "chat_type": "group",
  "text": "帮我总结一下今天的任务",
  "metadata": {
    "sender_name": "Alice",
    "is_mentioned": "true"
  },
  "attachments": [],
  "created_at": "2026-06-01T16:00:00+08:00"
}
```

Required fields:

- `id`: stable platform message ID, used for dedupe.
- `channel`: adapter name, such as `dingtalk`, `qq`, `wechat`, or a custom name.
- `text`: user-visible text to pass into Mateway.

Recommended fields:

- `account_id`: bot/account identity, useful when one adapter manages multiple accounts.
- `peer_id`: chat/user/room identity.
- `thread_id`: conversation identity; defaults to `peer_id` if omitted.
- `user_id`: sender identity.
- `chat_type`: usually `dm` or `group`.
- `attachments`: reserved list of media references; current implementation rejects non-empty attachments until media handling lands.

Session identity is derived as:

```text
{channel}:{account_id}:{peer_id}
```

If `account_id` is empty, Mateway uses:

```text
{channel}:{peer_id}
```

## Attachment Shape

The bridge event already reserves `attachments` so channel adapters can converge on one media envelope. Use this shape when implementing image support:

```json
{
  "type": "image",
  "url": "https://adapter.local/media/msg-001/image-1",
  "name": "image-1.jpg"
}
```

Planned fields:

- `type`: `image`, `voice`, `file`, or `video`.
- `url`: adapter-hosted download URL or signed platform URL.
- `name`: optional display filename.

Current behavior:

- `attachments: []` is accepted.
- Non-empty `attachments` are rejected in bridge v1.
- OpenClaw compatibility currently ignores non-text `MessageItem` values.

Recommended image implementation path:

1. Let the adapter download or proxy the platform image.
2. Expose a short-lived local or signed URL in `attachments[].url`.
3. Keep platform credentials inside the adapter.
4. Extend Mateway runtime to store media as evidence before passing it to tools or models.

## Receive Replies

There are two supported reply modes.

### Polling

Bridge integrations can poll:

```text
GET http://127.0.0.1:8789/channels/{channel}/replies
Authorization: Bearer <MATEWAY_BRIDGE_TOKEN>
```

Response:

```json
{
  "ok": true,
  "replies": [
    {
      "id": "reply-msg-001-20260601160000.000000000",
      "in_reply_to": "msg-001",
      "channel": "dingtalk",
      "peer_id": "chat-123",
      "thread_id": "chat-123",
      "text": "好的，我来总结。",
      "style": "completed",
      "metadata": {
        "locale": "zh-CN",
        "title": ""
      }
    }
  ]
}
```

### Callback

When posting an event, the adapter can provide:

```text
X-Mateway-Outbound-URL: http://127.0.0.1:9000/mateway/replies
```

Mateway will `POST` the reply payload to that URL. If no callback URL is known, replies are queued for polling.

## Optional Delivery Ack

Bridge integrations may report delivery result:

```text
POST http://127.0.0.1:8789/channels/{channel}/acks
Authorization: Bearer <MATEWAY_BRIDGE_TOKEN>
Content-Type: application/json
```

```json
{
  "id": "reply-msg-001-20260601160000.000000000",
  "status": "sent",
  "message": ""
}
```

This endpoint is currently accepted for observability and future extension; it does not change runtime state yet.

## Health Check

```text
GET http://127.0.0.1:8789/channels/{channel}/health
```

Returns:

```json
{
  "ok": true,
  "channel": "dingtalk"
}
```

## OpenClaw WeChat Bot Compatibility

Mateway also exposes a compatibility adapter for the OpenClaw WeChat plugin. This path is for `@tencent-weixin/openclaw-weixin`, also shown in WeChat as ClawBot.

Enable `~/.mateway/config/channels/openclaw_compat.yaml`:

```yaml
openclaw_compat:
  enabled: true
  addr: 127.0.0.1:8790
  path_prefix: /
  token: ""
  token_env: MATEWAY_OPENCLAW_COMPAT_TOKEN
  bot_agent: Mateway/0.1
  longpoll_timeout_ms: 35000
```

Put the token in `~/.mateway/config/mateway.env`:

```bash
MATEWAY_OPENCLAW_COMPAT_TOKEN=replace-with-a-random-token
```

Start Mateway:

```bash
mateway gateway serve
```

The compatibility adapter exposes:

- `POST /sendmessage`: receives text messages from the WeChat plugin and enters Mateway runtime.
- `POST /getupdates`: long-polls replies generated by Mateway.
- `POST /getconfig`: returns minimal config for plugin flow.
- `POST /sendtyping`: no-op success for typing indicators.

The adapter accepts OpenClaw-style headers:

```text
AuthorizationType: ilink_bot_token
Authorization: Bearer <MATEWAY_OPENCLAW_COMPAT_TOKEN>
```

### Current WeChat Setup Flow

Important: `@tencent-weixin/openclaw-weixin-cli install` checks for a local OpenClaw installation first. It is an OpenClaw plugin installer, not a standalone Mateway installer. Run it only in an environment where the `openclaw` CLI is already installed and configured.

Install the official WeChat ClawBot plugin in the environment that runs OpenClaw:

```bash
npx -y @tencent-weixin/openclaw-weixin-cli@latest install
```

Scan the QR code shown by the plugin flow in WeChat and enable ClawBot.

To use Mateway as the backend, configure the plugin/backend gateway URL to:

```text
http://127.0.0.1:8790/
```

Use the same bearer token as `MATEWAY_OPENCLAW_COMPAT_TOKEN` if the plugin asks for an ilink bot token.

After that:

1. WeChat ClawBot sends text messages to Mateway through `sendmessage`.
2. Mateway normalizes them as channel `openclaw-weixin`.
3. Gateway runs dedupe, session key, runtime, and AgentCore.
4. The plugin polls `getupdates`.
5. Mateway returns text replies with the original `context_token`.

Current limitations:

- Text only.
- No image, voice, file, video, CDN upload, or media download.
- No direct WeChat login implementation in Mateway.
- OpenClaw compatibility is an adapter, not Mateway's internal channel protocol.

## Writing A New Channel Adapter

For DingTalk, QQ, Telegram, Discord, a custom website chat, or any other platform:

1. Build a small process that receives platform messages.
2. Convert platform messages to the bridge event JSON.
3. Post events to `/channels/{channel}/events`.
4. Poll `/channels/{channel}/replies` or provide `X-Mateway-Outbound-URL`.
5. Send Mateway reply text back through the platform API.

Keep adapter-specific policy outside Mateway when possible:

- whether group chats require mention
- platform retry policy
- message formatting limits
- media upload/download
- account login and token refresh

Use Mateway metadata for traceable facts, not runtime control whenever possible.
