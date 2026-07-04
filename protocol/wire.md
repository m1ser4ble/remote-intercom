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

All frames are UTF-8 JSON text. Frames larger than 64 KiB are rejected.

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
