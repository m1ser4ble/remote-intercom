# ADR-0001: Ephemeral relay reliability model

## Status

Accepted

## Date

2026-07-19

## Context

Remote Intercom is an in-memory relay for live pi sessions, not a durable broker. Production investigation of issues #1–#3 found three gaps:

- preserving a channel after every member went offline produced an ownerless room where new devices could remain pending forever;
- message and join-decision tools returned success after a local socket write, before the relay confirmed the operation;
- a client socket could remain locally open while relay requests timed out, producing a silent half-open connection with no reconnect diagnostics.

The original product design defined channels as ephemeral: reconnect within grace preserves membership, while expiry of the last member deletes the channel. Commit `3441f88` changed the last step to preserve offline membership, contradicting that model and enabling the ownerless state.

The relay must become reliable enough for live coordination without acquiring the storage, deduplication, and operational complexity of a durable messaging system.

## Decision

Keep Remote Intercom ephemeral and non-durable, with these guarantees:

1. **Channel lifecycle** — reconnect grace remains 30 seconds. When the final online member exceeds grace, delete the channel, membership history, and pending joins. The next connection for the same name and PIN creates a new channel and owner.
2. **Join authorization** — pending devices may request their status and receive approval or denial. They may not list members, send messages, or decide joins.
3. **Message success** — `send`, `ask`, and `reply` succeed only after the relay writes the event to the target WebSocket and returns a correlated `message.ack`.
4. **Join-decision success** — approval and denial succeed only after the relay applies the decision and returns a correlated `join.decision.ack`.
5. **Acknowledgement boundary** — a message ACK confirms target socket write only. It does not confirm that a user or agent processed or read the message.
6. **Unknown outcomes** — clients do not automatically replay messages after ACK timeout because the original write may have succeeded.
7. **Frame limit** — clients reject a complete serialized UTF-8 event larger than 64 KiB before writing it to the socket.
8. **Liveness** — members and pending devices use a 25-second application status heartbeat with a 5-second response timeout. A failed heartbeat marks the transport stale and starts the existing bounded reconnect backoff. Tool actions probe or restore stale connections before proceeding.
9. **Explicit disconnect** — an explicit disconnect stops heartbeat and reconnect.

## Alternatives considered

### Preserve ownerless channels indefinitely

This keeps admitted-device history but leaves new joiners unable to obtain approval when no admitted member is online. It also contradicts the original live-room model.

Rejected because it preserves the production deadlock from issue #1.

### Preserve channels for a longer dormant TTL

A dormant TTL could allow late reconnects while eventually removing ownerless channels.

Rejected for the MVP because it adds another timer state and policy without a demonstrated need. The existing reconnect grace already defines the recovery window.

### Introduce durable queues, chunk transfer, and processing receipts

Persistence and deduplication could provide stronger delivery guarantees and automatic replay.

Rejected because they require a datastore, retention policy, idempotency contract, and operational model beyond a live ephemeral relay.

### Reconnect only after the next user action

Lazy recovery is simpler but misses messages while an idle socket is half-open.

Rejected because issue #3 is specifically a long-running idle-session failure.

## Consequences

- Returning admitted devices must reconnect within grace to preserve membership.
- A device returning after channel deletion may create a new channel and become owner.
- Callers can distinguish a confirmed socket delivery from a rejected or unknown result.
- ACK timeouts remain ambiguous by design and require caller judgment rather than automatic replay.
- The relay and extension gain correlated ACK events and application heartbeat logic but no new dependency or datastore.
- Wire behavior, product documentation, and tests must remain synchronized with this ADR.
