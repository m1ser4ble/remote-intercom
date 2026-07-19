# Remote Intercom wire protocol

The relay uses HTTP for bootstrap and WebSocket text frames for live session traffic. JSON field names are camelCase.

## HTTP endpoints

### `GET /healthz`

Returns `200 OK` with `ok\n` when the relay process is alive.

### `GET /version`

```json
{"version":"0.1.0"}
```

### `GET /install.sh`

Returns a shell installer script. The relay infers public URLs from `Host` and `X-Forwarded-Proto` unless configured with public URLs by the embedding server.

The rendered script contains:

```sh
RELAY_HTTP_URL='https://relay.example.com'
RELAY_WS_URL='wss://relay.example.com/ws'
EXTENSION_URL='https://relay.example.com/extension.mjs'
```

The script writes relay configuration and installs the bundled pi extension into the pi extension discovery directory.

### `GET /extension.mjs`

Returns the bundled single-file pi extension served from the relay operator's `RELAY_EXTENSION_BUNDLE` path. If no bundle is configured, the endpoint returns `404` JSON error.

### `POST /channels/connect`

Request:

```json
{
  "channelName": "ops",
  "pin": "123456",
  "deviceName": "alice-laptop",
  "deviceId": "dev_alice",
  "clientVersion": "0.1.0"
}
```

Response for the first device:

```json
{
  "status": "created",
  "channelId": "ch_...",
  "deviceId": "dev_alice",
  "token": "eyJ...",
  "wsUrl": "wss://relay.example.com/ws"
}
```

Response for a returning admitted member:

```json
{
  "status": "connected",
  "channelId": "ch_...",
  "deviceId": "dev_alice",
  "token": "eyJ...",
  "wsUrl": "wss://relay.example.com/ws"
}
```

Response for a new device joining an existing channel:

```json
{
  "status": "pending_approval",
  "channelId": "ch_...",
  "deviceId": "dev_bob",
  "joinRequestId": "join_...",
  "token": "eyJ...",
  "wsUrl": "wss://relay.example.com/ws"
}
```

Errors use JSON:

```json
{"error":"channelName, pin, and deviceName are required"}
```

Default resource limits are 32 admitted members and 16 pending joins per channel.

## WebSocket

Connect to `wsUrl` with either:

- `Authorization: Bearer <token>`
- `?token=<token>` query parameter

All frames are UTF-8 JSON text. The complete serialized frame, including the event envelope, must not exceed 65,536 bytes. Clients reject oversized outbound events before socket write. The relay enforces the same limit both when reading client frames and after adding routing fields, before writing to the target socket.

```json
{
  "id": "evt_1",
  "type": "message.send",
  "channelId": "ch_...",
  "from": "dev_alice",
  "to": "dev_bob",
  "replyTo": "evt_0",
  "payload": {}
}
```

Clients may omit `channelId` and `from`; the relay fills or validates them from the token.

### Role permissions

| Event | Member | Pending |
|---|---:|---:|
| `status.request` | yes | yes |
| `list.request` | yes | no |
| `message.send` / `message.ask` / `message.reply` | yes | no |
| `message.broadcast` | yes | no |
| `join.approve` / `join.deny` | current owner only | no |
| Receive `join.approved` / `join.denied` | n/a | yes |

Disallowed requests receive a correlated `error` with code `unauthorized`.

## Event types

### `message.send`

Direct message to one online device.

```json
{
  "id": "evt_101",
  "type": "message.send",
  "to": "dev_bob",
  "payload": {"text":"hello","kind":"send"}
}
```

### `message.ask`

Question/request to one online device. Payload markers such as `kind` are optional compatibility metadata; the event type is first-class.

```json
{
  "id": "evt_102",
  "type": "message.ask",
  "to": "dev_bob",
  "payload": {"text":"approve deploy?","kind":"ask"}
}
```

### `message.reply`

Reply to an earlier event. The relay routes it like a direct message and preserves `replyTo`.

```json
{
  "id": "evt_103",
  "type": "message.reply",
  "to": "dev_alice",
  "replyTo": "evt_102",
  "payload": {"text":"approved","kind":"reply"}
}
```

### `message.broadcast`

Broadcast to other online members in the channel.

```json
{
  "id": "evt_201",
  "type": "message.broadcast",
  "payload": {"text":"standup starting"}
}
```

### `message.ack`

For `message.send`, `message.ask`, and `message.reply`, the relay acknowledges the sender only after writing the original event to the target WebSocket:

```json
{
  "type": "message.ack",
  "channelId": "ch_...",
  "to": "dev_alice",
  "replyTo": "evt_101",
  "payload": {
    "status": "delivered",
    "deviceId": "dev_bob"
  }
}
```

This confirms target socket write, not agent processing or user read. A client that times out waiting for this ACK reports an unknown delivery outcome and does not automatically replay the message.

### `join.request`

Relay sends this to the current owner when a pending device is connected.

```json
{
  "type": "join.request",
  "channelId": "ch_...",
  "from": "dev_bob",
  "to": "dev_alice",
  "payload": {
    "joinRequestId": "join_...",
    "deviceId": "dev_bob",
    "deviceName": "bob-laptop"
  }
}
```

### `join.approve` / `join.deny`

Only the current owner can decide join requests.

```json
{
  "id": "evt_301",
  "type": "join.approve",
  "payload": {"joinRequestId":"join_..."}
}
```

```json
{
  "id": "evt_302",
  "type": "join.deny",
  "payload": {"joinRequestId":"join_..."}
}
```

The pending device receives one of:

```json
{
  "type": "join.approved",
  "channelId": "ch_...",
  "from": "dev_alice",
  "to": "dev_bob",
  "replyTo": "evt_301",
  "payload": {"joinRequestId":"join_...","deviceId":"dev_bob"}
}
```

```json
{
  "type": "join.denied",
  "channelId": "ch_...",
  "from": "dev_alice",
  "to": "dev_bob",
  "replyTo": "evt_302",
  "payload": {"joinRequestId":"join_...","deviceId":"dev_bob"}
}
```

After applying the decision, the relay confirms it to the owner:

```json
{
  "type": "join.decision.ack",
  "channelId": "ch_...",
  "to": "dev_alice",
  "replyTo": "evt_301",
  "payload": {
    "joinRequestId": "join_...",
    "deviceId": "dev_bob",
    "decision": "approved"
  }
}
```

`decision` is `approved` or `denied`. Owner tools report success and remove local pending state only after this ACK.

### `list.request` / `list.response`

```json
{"id":"evt_401","type":"list.request"}
```

```json
{
  "type": "list.response",
  "channelId": "ch_...",
  "to": "dev_alice",
  "replyTo": "evt_401",
  "payload": {
    "ownerId": "dev_alice",
    "members": [
      {"deviceId":"dev_alice","deviceName":"alice-laptop","online":true,"owner":true},
      {"deviceId":"dev_bob","deviceName":"bob-laptop","online":true,"owner":false}
    ]
  }
}
```

### `status.request` / `status.response`

```json
{"id":"evt_501","type":"status.request"}
```

```json
{
  "type": "status.response",
  "channelId": "ch_...",
  "to": "dev_alice",
  "replyTo": "evt_501",
  "payload": {
    "status": "member",
    "channelId": "ch_...",
    "deviceId": "dev_alice",
    "ownerId": "dev_alice"
  }
}
```

## Liveness and recovery

The relay sends protocol-level WebSocket ping frames for server-side liveness. Clients additionally send an application `status.request` every 25 seconds and require a matching `status.response` within 5 seconds.

A heartbeat timeout means the transport is half-open or non-responsive even when its local `readyState` is still open. The client marks it stale, emits a diagnostic, closes the stale socket, and starts bounded reconnect. This applies to members and pending devices. Explicit disconnect disables heartbeat and reconnect.

Before a network tool action, a stale client probes or restores the connection. A message written before its ACK times out is never automatically replayed.

## Channel expiry

Member disconnect starts the 30-second reconnect grace. A reconnect during grace preserves membership and priority. When the last online member exceeds grace, the relay deletes the channel, membership history, and pending joins. A later connection using the same name and PIN creates a new channel.

### `error`

```json
{
  "type": "error",
  "channelId": "ch_...",
  "to": "dev_alice",
  "replyTo": "evt_bad",
  "payload": {
    "code": "invalid_event",
    "message": "unknown event type"
  }
}
```
