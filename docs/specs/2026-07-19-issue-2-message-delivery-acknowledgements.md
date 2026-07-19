# Spec: Issue #2 — Message delivery acknowledgements

## Objective

Make `send`, `ask`, and `reply` results accurately represent relay delivery to the target WebSocket, and reject oversized events before they terminate the transport.

The relay remains a live, non-durable router. Success means that the relay completed a target socket write; it does not mean that the target agent or user processed the message.

## Observed failure

Production tests on 2026-07-19 showed:

- five consecutive messages of approximately 12 KiB were all delivered;
- a 60,000-character frame was delivered;
- incompressible 70,000- and 266,668-character frames returned immediately from the client, were not delivered, and later closed the socket with code `1009` at 65,537 bytes;
- an unknown target also returned immediately before a later correlated `unknown_target` error.

The original report of missing 12 KiB chunks was not reproduced. The confirmed defect is that tool success precedes relay delivery outcome and that the client does not enforce the documented frame limit.

## Tech stack

- Go WebSocket relay.
- TypeScript extension client and tool adapter.
- Go `testing` and Vitest.
- No new dependencies, storage, compression policy, or chunk protocol.

## Commands

```bash
cd relay && go test ./internal/ws ./internal/integration
cd extension && npm test
cd relay && go test ./...
cd extension && npm run build:bundle
```

## Project structure

- `relay/internal/ws/hub.go` — target write and ACK/error emission.
- `extension/src/client/protocol.ts` — message ACK payload.
- `extension/src/client/relay-client.ts` — frame-size validation and correlated ACK waiters.
- `extension/src/tools/intercom-tool.ts` — asynchronous message tool results.
- Adjacent `*_test.go` and `*.test.ts` files — unit and integration tests.
- `protocol/wire.md` — normative size and ACK contract.

## Behavior and wire contract

### Outbound frame validation

Before `WebSocket.send`, serialize the complete outbound event exactly once and compute its UTF-8 byte length. If it exceeds 65,536 bytes, reject locally with:

```text
code: message_too_large
limitBytes: 65536
actualBytes: <serialized byte length>
```

The calculation includes envelope fields and handles multibyte text. The relay read limit remains defense in depth.

### Delivery acknowledgement

After `target.writeEvent(event)` succeeds, the relay returns to the sender:

```json
{
  "type": "message.ack",
  "replyTo": "original-message-id",
  "to": "sender-device-id",
  "payload": {
    "status": "delivered",
    "deviceId": "target-device-id"
  }
}
```

The client resolves the operation only when `message.ack.replyTo` matches the outbound ID.

If target lookup or target write fails, the existing correlated `error` event rejects the operation. Error codes remain explicit, including `unknown_target` and `target_unreachable`.

### Timeout and replay

The default ACK timeout is 10 seconds, longer than the relay's 5-second target write timeout. Timeout rejects with `delivery_unknown`: the relay may have delivered the event and lost the ACK. The client may restore transport health for later actions but must not replay the message automatically.

`send`, `ask`, and `reply` become asynchronous client/tool operations. Their successful result includes the outbound event and the ACK event.

### Chunking

No automatic chunking or reassembly is added. Callers must remain under the frame limit. A future chunk protocol requires a separate spec and ADR because it needs transfer identity, ordering, checksums, timeout, and duplicate handling.

## Code style

Reuse the current correlation and waiter structure:

```ts
const response = await client.send(to, message);
return {
  event: response.requestEvent,
  responseEvent: response.responseEvent,
};
```

Do not create a generic command bus or add a dependency for byte counting.

## Testing strategy

Follow RED → GREEN → REFACTOR:

1. Add a client test proving an oversized serialized UTF-8 frame fails before `socket.send`.
2. Cover envelope overhead, exact-limit behavior, and multibyte text.
3. Add a hub test proving ACK is emitted only after successful target write.
4. Add relay tests proving unknown and unreachable targets produce correlated errors and no ACK.
5. Add client/tool tests proving success waits for `message.ack`.
6. Add timeout tests proving `delivery_unknown` and zero automatic resend.
7. Add an integration test sending five unique approximately 12 KiB messages and asserting five receives plus five correlated ACKs.
8. Keep normal 60,000-character delivery below the complete serialized frame limit covered.

Tests use real serialization and relay routing where practical; mocks are limited to socket boundaries.

## Boundaries

Always:

- measure the complete serialized frame;
- preserve unique message IDs and `replyTo` correlation;
- distinguish confirmed socket delivery from unknown outcome.

Ask first:

- changing the 64 KiB limit or ACK timeout;
- adding another delivery guarantee.

Never:

- report success before ACK;
- automatically replay an unknown message;
- claim user processing/read confirmation;
- add automatic chunking in this change.

## Success criteria

- Oversized events fail locally without closing the socket.
- A successful message tool result contains a correlated `message.ack`.
- Unknown/unreachable target and ACK timeout paths fail visibly.
- ACK timeout sends the original event exactly once.
- Five rapid approximately 12 KiB messages produce five receives and five ACKs.
- Existing send/ask/reply routing behavior remains compatible below the limit.
- Relevant and full Go/TypeScript suites pass.

## Open questions

None. Product decisions were confirmed on 2026-07-19.
