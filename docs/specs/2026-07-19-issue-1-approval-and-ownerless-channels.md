# Spec: Issue #1 — Approval and ownerless channels

## Objective

Prevent ownerless channels from trapping new devices in `pending_approval`, prevent approval calls from appearing successful before relay confirmation, and restrict pending devices to the minimum information and actions needed to complete a join.

A successful implementation satisfies these user-visible outcomes:

- the final member may reconnect during the existing 30-second grace period;
- after grace expires, the channel and pending joins are deleted;
- a later connection for the same channel name and PIN creates a fresh channel and owner;
- join approval and denial return success only after relay acknowledgement;
- pending devices cannot list channel membership.

## Observed failure

Production reproduction on 2026-07-19:

1. Create a channel and connect its owner.
2. Disconnect the owner and wait 32 seconds.
3. Connect a new device with the same channel name and PIN.
4. Observe `pending_approval`, `ownerId: ""`, and one offline non-owner member.
5. Send `join.approve` from the pending connection.
6. Observe an immediate local event followed by asynchronous `unauthorized`; status remains pending.

This is not automatic admission. It is a false success surface combined with an ownerless channel.

## Tech stack

- Go relay registry and WebSocket hub.
- TypeScript extension client and tool adapter.
- Go `testing` and Vitest.
- No new dependencies or persistent storage.

## Commands

```bash
cd relay && go test ./internal/channel ./internal/ws ./internal/integration
cd extension && npm test
cd relay && go test ./...
cd extension && npm run build:bundle
```

## Project structure

- `relay/internal/channel/registry.go` — channel lifecycle and owner election.
- `relay/internal/ws/hub.go` — permissions, channel deletion effects, and join decision events.
- `extension/src/client/protocol.ts` — join decision ACK types.
- `extension/src/client/relay-client.ts` — correlated decision request/response.
- `extension/src/tools/intercom-tool.ts` — await ACK and mutate local pending state only after success.
- Adjacent `*_test.go` and `*.test.ts` files — regression tests.

## Behavior and wire contract

### Channel lifecycle

`ExpireOfflineMember` deletes the channel when recomputing ownership leaves no online owner. Deletion removes the channel key and membership history. The hub closes pending sockets for the deleted channel with a clear channel-deleted reason.

Reconnect before grace expiry cancels deletion and preserves existing priority. Reconnect after deletion follows first-connect behavior and creates a new channel.

### Pending permissions

A pending connection may:

- send `status.request`;
- receive `join.approved`, `join.denied`, `error`, and connection lifecycle events.

A pending connection may not:

- send `list.request`;
- send, ask, reply, or broadcast messages;
- approve or deny joins.

Disallowed requests receive a correlated `error` with code `unauthorized`.

### Join decision acknowledgement

After the relay successfully applies an owner decision, it returns:

```json
{
  "type": "join.decision.ack",
  "replyTo": "decision-event-id",
  "to": "owner-device-id",
  "payload": {
    "joinRequestId": "join_...",
    "deviceId": "target-device-id",
    "decision": "approved"
  }
}
```

`decision` is `approved` or `denied`.

The tool reports success and removes the local pending request only after this ACK. The decision timeout is 10 seconds so relay writes bounded by the existing 5-second write timeout can finish before the client gives up. A correlated error or `decision_unknown` timeout leaves local pending state intact.

## Code style

Follow existing explicit event construction and error correlation patterns:

```go
_ = c.writeEvent(protocol.Event{
    Type:    "join.decision.ack",
    To:      c.deviceID,
    ReplyTo: event.ID,
    Payload: map[string]any{"joinRequestId": joinRequestID},
})
```

Prefer direct branches and existing helpers over new abstractions.

## Testing strategy

Follow RED → GREEN → REFACTOR for each behavior:

1. Restore a failing registry test asserting that expiry of the last member deletes the channel key.
2. Add a hub test asserting that channel deletion closes pending connections.
3. Add a hub test asserting pending `list.request` returns correlated `unauthorized` while `status.request` remains allowed.
4. Add relay tests for successful approval and denial ACKs.
5. Add extension tests proving decision tools remain pending until ACK.
6. Add extension tests proving correlated error and timeout retain local pending state.
7. Keep normal approval, failover, and reconnect-within-grace tests green.

Tests assert externally visible state and events, not helper call order.

## Boundaries

Always:

- preserve owner-only decision authority;
- preserve status access for pending devices;
- correlate every ACK/error to the initiating event ID.

Ask first:

- changing reconnect grace;
- exposing additional channel data to pending devices.

Never:

- automatically approve a device;
- preserve an ownerless channel after final grace expiry;
- delete local pending state before relay acknowledgement.

## Success criteria

- No channel exists after the last member exceeds reconnect grace.
- Pending sockets are closed when their channel is deleted.
- A reconnect within grace preserves membership; a reconnect after deletion creates a fresh channel.
- Pending `list.request` is denied and pending `status.request` succeeds.
- Join decision tools return success only after `join.decision.ack`.
- Error and timeout paths retain local pending state.
- Relevant and full Go/TypeScript suites pass.

## Open questions

None. Product decisions were confirmed on 2026-07-19.
