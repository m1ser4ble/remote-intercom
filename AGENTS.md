# Remote Intercom Agent Contract

This file contains stable rules for humans and coding agents working in this repository. Keep it short; detailed behavior belongs in specs and protocol documentation.

## Source of truth

Use these sources in order:

1. `AGENTS.md` — repository-wide invariants and workflow.
2. `docs/decisions/` — accepted architectural decisions and their rationale.
3. `docs/specs/` — change-specific behavior and acceptance criteria.
4. `protocol/wire.md` — normative wire protocol.
5. Source and tests — implementation and executable evidence.

If two sources conflict, stop and resolve the higher-level document before changing code.

## Commands

```bash
cd relay && go test ./...
cd extension && npm test
cd extension && npm run build:bundle
```

## Required workflow

- For every behavior change or bug fix, write one focused failing test first and verify that it fails for the expected reason.
- Write the minimum production change that makes the test pass, then run the relevant suite and the full suite.
- Do not remove, skip, or weaken a failing test to make a change pass.
- Keep protocol documentation and the relevant spec synchronized with wire behavior.
- Do not edit generated `extension/dist/` output as source.

## Reliability invariants

- The relay is ephemeral and in-memory. It is not a durable message broker.
- When the last online member exceeds reconnect grace, delete the channel and its membership history.
- Pending devices may request status and receive join decisions. They may not list members, send messages, or decide joins.
- `send`, `ask`, and `reply` succeed only after a correlated `message.ack` confirms a successful target WebSocket write.
- A message ACK does not mean that the target user or agent processed the message.
- Never automatically replay a message whose delivery outcome is unknown; doing so can duplicate side effects.
- Reject an outbound event before socket write when its complete serialized UTF-8 frame exceeds 64 KiB.
- Detect half-open connections with application-level heartbeat requests. Apply liveness and reconnect behavior to members and pending devices.
- An explicit disconnect cancels heartbeat and automatic reconnect.

## Boundaries

Always:

- Preserve correlation IDs across request, ACK, and error events.
- Keep normal owner approval, owner failover, and reconnect-within-grace behavior covered by tests.
- Prefer existing Go and TypeScript dependencies and existing request/response machinery.

Ask first:

- Adding a dependency or datastore.
- Changing the 64 KiB wire limit or reconnect timing defaults.
- Changing public event names, delivery guarantees, or channel identity semantics.

Never without a new accepted ADR:

- Add durable queues, automatic chunking, automatic message replay, or persistent channels.
- Redefine socket-delivery ACK as user-processing or read confirmation.
