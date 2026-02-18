# Gateway WebSocket Protocol (Draft)

This document defines the WebSocket message envelope and event types used between:

- **Client** (UI / CLI / automation)
- **Gateway** (OpenClaw gateway / orchestrator)
- **Agent runtime** (LLM worker)
- **Tools** (taskboard, discord, browser, nodes, etc.)

> Status: draft. The goal is a stable envelope + versioning scheme, with extensible typed payloads.

---

## 1) Transport

- Protocol: WebSocket
- Encoding: UTF-8 JSON
- Framing: 1 JSON object per message

### Constraints

- Messages MUST be valid JSON objects.
- Messages SHOULD be < 256KB. Large payloads (e.g., screenshots/audio) should be referenced by URL/object-store key.

---

## 2) Envelope

Every message MUST follow this envelope:

```json
{
  "v": 1,
  "type": "<string>",
  "id": "<uuid or ulid>",
  "ts": "<ISO-8601 UTC timestamp>",
  "session": {
    "sessionId": "<string>",
    "userId": "<string>",
    "agentId": "<string>"
  },
  "correlationId": "<string|null>",
  "replyTo": "<string|null>",
  "meta": {
    "source": "client|gateway|agent|tool",
    "traceId": "<string|null>"
  },
  "payload": {}
}
```

### Field semantics

- `v` (number): protocol version.
- `type` (string): event type (see §4).
- `id` (string): unique message id.
- `ts` (string): ISO-8601 timestamp.
- `session`: routing context.
  - `sessionId`: conversation/session key.
  - `userId`: authenticated human user.
  - `agentId`: selected agent (if applicable).
- `correlationId` (string|null): ties multiple events to the same “operation” (e.g., one tool call producing streaming output).
- `replyTo` (string|null): references the `id` of the message being responded to.
- `meta.source`: where this message originated.
- `meta.traceId`: optional distributed trace id.
- `payload`: type-specific payload.

---

## 3) Auth & lifecycle

### 3.1 Authentication

Client sends an auth message immediately after connecting:

```json
{
  "v": 1,
  "type": "auth.request",
  "id": "...",
  "ts": "...",
  "session": {"sessionId": "", "userId": "", "agentId": ""},
  "correlationId": null,
  "replyTo": null,
  "meta": {"source": "client", "traceId": null},
  "payload": {"token": "<bearer>", "client": {"name": "", "version": ""}}
}
```

Gateway responds:

- `auth.ok` with user identity + capabilities
- or `auth.error` with reason

### 3.2 Heartbeat

- Client SHOULD send `ping` every 30s.
- Gateway replies with `pong`.

---

## 4) Event types (initial set)

### 4.1 Session / UX

- `session.join`
- `session.left`
- `session.state` (current status snapshot)
- `notice` (non-fatal notification)

### 4.2 Chat

- `chat.user_message`
- `chat.assistant_message`
- `chat.delta` (streaming token/content chunks)
- `chat.final` (final assistant message)

Payload notes:

- streaming events SHOULD use `correlationId` to group deltas.

### 4.3 Tools

- `tool.call`
- `tool.progress`
- `tool.result`
- `tool.error`

`tool.call.payload`:

```json
{
  "tool": "taskboard.create|discord.read|browser.snapshot|...",
  "args": {},
  "timeoutMs": 60000
}
```

### 4.4 Agent control

- `agent.spawn`
- `agent.cancel`
- `agent.status`

### 4.5 Intervention / approvals

- `approval.request`
- `approval.granted`
- `approval.denied`

Use this for actions that require human confirmation (posting externally, deletions, payments, etc.).

---

## 5) Errors

All error messages SHOULD use a consistent shape:

```json
{
  "code": "<string>",
  "message": "<human-readable>",
  "details": {},
  "retryable": false
}
```

---

## 6) Versioning

- Increment `v` only on breaking changes.
- Backward-compatible additions (new event types, new optional fields) do not require bumping `v`.

---

## 7) Open questions

- Do we need binary frames for screenshots/audio, or always URL references?
- How do we represent multi-tab / multi-device clients cleanly (additional `clientId`)?
- Should `session` include `workspaceId` / `guildId` for Discord contexts?
