# Concept

Remote Intercom is a relay-backed channel service for pi sessions that cannot reach each other through local IPC or SSH.

The product idea is simple: a pi user opens the same human-friendly channel from two machines, such as `dwkim 1234`, and the relay connects those machines as if they were on the same intercom mesh. The user should not need to copy long tokens, manage SSH access, or operate a separate admin panel. The pi agent conversation is the primary interface.

## What it is

Remote Intercom is three things packaged together:

1. **Channel relay** — a Go service that keeps ephemeral live channels, tracks members, routes messages, and manages owner approval.
2. **Pi extension/client** — a TypeScript client/tool layer that exposes connect, list, send, ask, reply, pending, status, and join approval actions to pi.
3. **Relay-served installer** — `/install.sh` is served by the relay itself, so public and self-hosted relays can provide a one-line setup path that writes the correct relay URLs locally.

## Core concept

A channel is identified by a channel name and PIN:

```text
dwkim 1234
```

- If no active channel exists for that pair, the first device creates it and becomes owner.
- If the channel exists, new devices become pending join requests.
- The current owner approves or denies the request through the pi agent flow.
- Owner is not a fixed account. It is the highest-priority online member.
- If the owner disconnects, ownership fails over to the next online member.
- If the original owner reconnects while the channel still exists, it becomes owner again.
- If all members leave and reconnect grace expires, the channel and its membership history are deleted.
- Pending devices can check their own status and receive a join decision, but cannot list members, exchange messages, or decide joins.

This makes channels **live rooms**, not permanent server-side accounts.

## Design direction

Remote Intercom intentionally avoids a separate management UI or server admin API for channel control. The relay is a narrow router and state holder; decisions belong to the owner agent and its user.

The design favors:

- **Human-friendly setup** over long shared secrets.
- **Owner approval** over public self-service joins.
- **Ephemeral channel state** over durable room databases.
- **HTTP bootstrap** for predictable connect/token flows.
- **WebSocket live sessions** for presence, owner changes, join requests, and low-latency intercom messages.
- **Self-hostability** so sensitive users can run the relay where they trust the operator.

## Why WebSocket

The relay needs to know who is live now. Presence and owner election depend on active connections, not just stored tokens. WebSocket gives the relay a clear live-session primitive:

```text
socket open + relay activity = connected
application heartbeat timeout = stale and reconnecting
socket gone past grace = offline
last member offline past grace = channel deleted
```

HTTP is still used where it fits better:

- installer rendering;
- health/version checks;
- channel connect/bootstrap;
- signed token issuance.

## Live delivery contract

Remote Intercom confirms live socket delivery without pretending to be a durable queue:

- `send`, `ask`, and `reply` succeed only after the relay writes the event to the target WebSocket and returns a correlated acknowledgement.
- An acknowledgement means target socket write succeeded. It does not mean the target agent or user processed or read the message.
- If an acknowledgement times out, the outcome is unknown and the client does not automatically replay the message.
- The complete serialized UTF-8 WebSocket frame must be at most 64 KiB. Oversized events are rejected before socket write.
- Members and pending devices use application-level status heartbeats so a locally open but non-responsive connection is detected and reconnected.

Durable queues, automatic chunking, processing receipts, and cross-process connection persistence remain outside the MVP.

## Security posture

The MVP is transport-secured but not end-to-end encrypted.

- Use HTTPS/WSS in production.
- The relay operator can technically see routed message payloads.
- `channelName + PIN` is not treated as sufficient authorization by itself; active channels require owner approval.
- Tokens are signed relay credentials for WebSocket sessions, not values users need to copy.

For sensitive usage, self-host the relay until an E2EE design is added.

## Current boundaries

In scope for MVP:

- ephemeral channels;
- owner failover/restore;
- join approval;
- send, ask, reply, broadcast-style routing primitives;
- relay-served installer config;
- self-host docs and protocol docs.

Out of scope for MVP:

- durable channel accounts;
- admin/control API;
- file transfer;
- end-to-end encryption;
- public package publishing automation;
- large knowledge-base storage.
