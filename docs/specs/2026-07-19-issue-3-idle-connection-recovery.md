# Spec: Issue #3 — Idle connection recovery

## Objective

Detect and recover a half-open relay connection that remains locally `OPEN` but no longer receives relay responses, including while the user performs no intercom actions.

Recovery applies to admitted members and pending devices. Explicit disconnect remains final until the user connects again.

## Observed failure

A `dwkim` connection was established and then left idle. Without calling `connect` again, a later `status` request timed out waiting for `status.response`. The local inbox contained no `websocket_close`, `reconnect_scheduled`, or `reconnect_failed` diagnostics.

This confirms a half-open path distinct from an ordinary close event. Controlled relay closure with code `1009` already produces diagnostics and automatic reconnect; the missing behavior is active detection when no close/error event arrives.

## Tech stack

- TypeScript extension client using the existing WebSocket and HTTP bootstrap implementations.
- Existing Go `status.request`/`status.response` relay path.
- Vitest fake timers and mock WebSockets; Go integration tests only where relay behavior changes.
- No new heartbeat dependency or durable session service.

## Commands

```bash
cd extension && npm test
cd extension && npm run build:bundle
cd relay && go test ./...
```

## Project structure

- `extension/src/client/relay-client.ts` — activity tracking, heartbeat, stale state, and single-flight reconnect.
- `extension/src/client/protocol.ts` — transport diagnostics types when needed.
- `extension/src/tools/intercom-tool.ts` — healthy-connection preflight before network actions.
- Adjacent `*.test.ts` files — deterministic half-open and timer tests.
- `relay/internal/ws/hub.go` — existing status response and heartbeat behavior; change only if a failing integration test proves it necessary.

## Behavior contract

### Activity and heartbeat

Any successfully parsed inbound relay event records `lastRelayActivityAt`.

After a member or pending socket opens, start an application heartbeat:

```text
interval: 25 seconds
status response timeout: 5 seconds
maximum normal idle detection: approximately 30 seconds
```

Each heartbeat sends a uniquely identified `status.request`. A matching `status.response` records activity and keeps the connection healthy.

### Half-open recovery

If a heartbeat times out:

1. mark transport `stale`;
2. emit a `heartbeat_timeout` diagnostic containing channel and device IDs;
3. close/suppress the stale socket;
4. start the existing reconnect flow;
5. reuse the existing exponential backoff from 250 ms to a 5-second cap.

Reconnect is single-flight. Heartbeat, close handler, and concurrent tool actions must share one reconnect promise/timer rather than opening competing sockets.

Pending connections are reconnectable. Reusing their device ID returns the existing pending join and allows them to continue waiting for a decision.

### Tool preflight

Before `list`, `status`, `send`, `ask`, `reply`, `approve_join`, or `deny_join`:

- proceed immediately when transport is open and recently verified;
- when activity is stale, perform one status probe;
- if the probe fails, await reconnection before executing the operation once.

The `pending` tool action is local-only and never reconnects. `connect` establishes new logical connection state. `disconnect` cancels heartbeat, clears pending reconnect work, and prevents automatic recovery.

For message operations, preflight occurs before the first socket write. If the message was written and its ACK later times out, do not replay it.

### Diagnostics

Connection state exposes enough optional diagnostics for the tool result and inbox to explain recovery:

- `transportStatus`: `connected`, `stale`, `reconnecting`, or `disconnected`;
- `lastRelayActivityAt`;
- `lastDisconnectedAt`;
- last close code and reason when available;
- reconnect attempt.

Diagnostics must not expose or log the PIN.

## Code style

Use one timer, one reconnect promise, and existing request correlation:

```ts
if (this.reconnectPromise !== undefined) {
  return this.reconnectPromise;
}
```

Timer handles must be cleared on replacement and explicit disconnect. Avoid a separate heartbeat class unless tests show the client cannot remain understandable without one.

## Testing strategy

Follow RED → GREEN → REFACTOR with fake timers:

1. Model a socket whose `readyState` remains `OPEN`, accepts writes, and never returns `status.response`; prove heartbeat marks it stale.
2. Prove heartbeat timeout schedules exactly one reconnect.
3. Prove concurrent heartbeat and tool preflight share one reconnect.
4. Prove pending connections reconnect after heartbeat failure.
5. Prove successful heartbeat refreshes activity and does not reconnect.
6. Prove explicit disconnect cancels timers and prevents reconnect.
7. Prove stale tool preflight restores the connection and executes the original operation once.
8. Prove a post-write message ACK timeout does not replay the message.
9. Keep existing close-code diagnostics and reconnect tests green.

Tests must not depend on wall-clock sleeps or the production relay.

## Boundaries

Always:

- use deterministic timers in tests;
- cover member and pending states;
- preserve existing bounded backoff;
- redact credentials from diagnostics.

Ask first:

- changing heartbeat timing;
- persisting connection credentials or state across Pi process restarts.

Never:

- treat a fresh Pi process as already connected;
- automatically replay messages after ambiguous ACK timeout;
- run competing reconnect loops;
- reconnect after explicit disconnect.

## Success criteria

- A locally open but non-responsive socket is detected without user activity in approximately 30 seconds.
- Detection emits a visible diagnostic and starts one reconnect flow.
- Members and pending devices recover through the same liveness policy.
- Network tool operations preflight stale transport and execute once after recovery.
- Explicit disconnect remains disconnected.
- Existing ordinary-close reconnect behavior remains green.
- Relevant and full TypeScript/Go suites pass.

## Open questions

None. Product decisions were confirmed on 2026-07-19.
