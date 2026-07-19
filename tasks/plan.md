# Remote Intercom Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the accepted lifecycle, acknowledgement, frame-limit, permission, and idle-recovery contracts for issues #1–#3 without adding persistence, chunking, replay, or dependencies.

**Architecture:** Preserve the current in-memory registry, Go WebSocket hub, TypeScript relay client, and tool adapter. Restore ephemeral channel deletion in the registry; add two targeted correlated ACK events in the hub; reuse the client's request waiter for delivery and decision results; add client-side serialized-byte validation and one application heartbeat with single-flight reconnect.

**Tech Stack:** Go 1.23+, `coder/websocket`, TypeScript, Node.js `ws`, Vitest, existing pi extension adapter.

**Canonical specs:**

- `docs/specs/2026-07-19-issue-1-approval-and-ownerless-channels.md`
- `docs/specs/2026-07-19-issue-2-message-delivery-acknowledgements.md`
- `docs/specs/2026-07-19-issue-3-idle-connection-recovery.md`
- `docs/decisions/0001-ephemeral-relay-reliability-model.md`

---

## File responsibility map

| File | Responsibility in this change |
|---|---|
| `relay/internal/channel/registry.go` | Delete the last-member channel after reconnect grace. |
| `relay/internal/channel/registry_test.go` | Prove ephemeral lifecycle and reconnect-after-delete behavior. |
| `relay/internal/ws/hub.go` | Enforce pending permissions; emit message and join-decision ACKs. |
| `relay/internal/ws/hub_test.go` | Prove permissions, ACK ordering/correlation, channel cleanup, and burst routing. |
| `relay/internal/integration/e2e_test.go` | Consume and verify new ACKs in the real end-to-end flow. |
| `extension/src/client/protocol.ts` | Define ACK event names and payload types. |
| `extension/src/client/relay-client.ts` | Validate frame bytes, await ACK/error, track liveness, and reconnect single-flight. |
| `extension/src/client/relay-client.test.ts` | Prove frame, ACK, timeout, heartbeat, pending reconnect, and disconnect behavior. |
| `extension/src/tools/intercom-tool.ts` | Await client operations and mutate pending state only after decision ACK. |
| `extension/src/tools/intercom-tool.test.ts` | Prove tool result and pending-state contracts. |
| `protocol/wire.md`, `docs/concept.md`, `AGENTS.md` | Already define the accepted behavior; adjust only if implementation reveals a concrete contradiction. |

## Dependency graph

```text
Task 1: registry lifecycle + pending permission
        │
        ├── Task 2: relay ACK events
        │          │
        │          ├── Task 3: client ACK + frame limit
        │          │          │
        │          │          └── Task 4: tool ACK semantics
        │          │
        │          └── Task 6: end-to-end ACK/burst verification
        │
        └── Task 5: heartbeat + single-flight reconnect
                   │
                   └── Task 6: complete verification
```

---

### Task 1: Restore ephemeral channel expiry and pending permissions

**Files:**

- Modify: `relay/internal/channel/registry_test.go`
- Modify: `relay/internal/channel/registry.go`
- Modify: `relay/internal/ws/hub_test.go`
- Modify: `relay/internal/ws/hub.go`

**Acceptance criteria:**

- The final offline member deletes the channel after grace.
- The same name/PIN creates a fresh channel after deletion.
- Pending `status.request` works; pending `list.request` returns correlated `unauthorized`.

- [ ] **Step 1: Replace the preserving lifecycle test with the failing deletion contract**

In `relay/internal/channel/registry_test.go`, replace `TestExpireLastOfflineMemberPreservesChannelMembership` with:

```go
func TestExpireLastOfflineMemberDeletesChannelKey(t *testing.T) {
    r := NewRegistry()
    created := r.Connect("dwkim", "1234", "dev_a", "macbook")
    if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
        t.Fatal(err)
    }

    change, err := r.ExpireOfflineMember(created.Channel.ID, "dev_a")
    if err != nil {
        t.Fatal(err)
    }
    if !change.Deleted {
        t.Fatal("expected channel deletion")
    }
    if ch := r.Channel(created.Channel.ID); ch != nil {
        t.Fatalf("channel remains after deletion: %+v", ch)
    }

    fresh := r.Connect("dwkim", "1234", "dev_a", "macbook")
    if fresh.Status != StatusCreated {
        t.Fatalf("fresh status = %s, want %s", fresh.Status, StatusCreated)
    }
}
```

- [ ] **Step 2: Run the registry test and verify RED**

Run:

```bash
cd relay
go test ./internal/channel -run TestExpireLastOfflineMemberDeletesChannelKey -count=1
```

Expected: FAIL with `expected channel deletion` because `ExpireOfflineMember` currently preserves the channel.

- [ ] **Step 3: Restore deletion in the registry**

In `relay/internal/channel/registry.go`, change only `ExpireOfflineMember`:

```go
func (r *Registry) ExpireOfflineMember(channelID, deviceID string) (*PresenceChange, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    ch, ok := r.channels[channelID]
    if !ok {
        return nil, fmt.Errorf("channel %q not found", channelID)
    }
    return r.setOnlineLocked(ch, deviceID, false, true)
}
```

- [ ] **Step 4: Run the registry test and verify GREEN**

Run:

```bash
cd relay
go test ./internal/channel -run TestExpireLastOfflineMemberDeletesChannelKey -count=1
```

Expected: PASS.

- [ ] **Step 5: Replace the hub preservation test and add pending cleanup/permission tests**

In `relay/internal/ws/hub_test.go`, replace `TestLastMemberDisconnectGracePreservesChannelMembership` with:

```go
func TestLastMemberDisconnectGraceDeletesChannel(t *testing.T) {
    fixture := newRelayFixture(t, 25*time.Millisecond)
    alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
    aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)

    if err := aliceWS.Close(websocket.StatusNormalClosure, "disconnect alice"); err != nil {
        t.Fatal(err)
    }
    waitForCondition(t, func() bool { return fixture.registry.Channel(alice.ChannelID) == nil })

    fresh := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
    if fresh.Status != string(channel.StatusCreated) {
        t.Fatalf("fresh status = %q, want %q", fresh.Status, channel.StatusCreated)
    }
}
```

Add pending cleanup:

```go
func TestLastMemberExpiryClosesPendingConnection(t *testing.T) {
    fixture := newRelayFixture(t, 25*time.Millisecond)
    alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
    aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
    bob := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
    bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
    _ = readEventOfType(t, aliceWS, "join.request", nil)

    if err := aliceWS.Close(websocket.StatusNormalClosure, "disconnect last owner"); err != nil {
        t.Fatal(err)
    }
    waitForCondition(t, func() bool { return fixture.registry.Channel(alice.ChannelID) == nil })
    assertCloseStatus(t, bobWS, websocket.StatusPolicyViolation)
}
```

Add pending permissions:

```go
func TestPendingConnectionCanRequestStatusButCannotListMembers(t *testing.T) {
    fixture := newRelayFixture(t, 50*time.Millisecond)
    alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
    aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
    bob := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
    bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
    _ = readEventOfType(t, aliceWS, "join.request", nil)

    writeEvent(t, bobWS, protocol.Event{ID: "pending-status", Type: "status.request"})
    status := readEventOfType(t, bobWS, "status.response", func(event protocol.Event) bool {
        return event.ReplyTo == "pending-status"
    })
    if got := stringPayload(t, status.Payload, "status"); got != "pending_approval" {
        t.Fatalf("status = %q, want pending_approval", got)
    }

    writeEvent(t, bobWS, protocol.Event{ID: "pending-list", Type: "list.request"})
    denied := readEventOfType(t, bobWS, "error", func(event protocol.Event) bool {
        return event.ReplyTo == "pending-list"
    })
    if got := stringPayload(t, denied.Payload, "code"); got != "unauthorized" {
        t.Fatalf("error code = %q, want unauthorized", got)
    }
}
```

- [ ] **Step 6: Run the hub tests and verify RED**

Run:

```bash
cd relay
go test ./internal/ws -run 'TestLastMemberDisconnectGraceDeletesChannel|TestLastMemberExpiryClosesPendingConnection|TestPendingConnectionCannotListMembers' -count=1
```

Expected: lifecycle tests fail while preservation remains; pending list test receives `list.response` instead of `unauthorized`.

- [ ] **Step 7: Deny pending list requests in the hub**

At the beginning of `sendList` in `relay/internal/ws/hub.go`, add the same role guard used by message routing:

```go
func (h *Hub) sendList(c *connection, replyTo string) {
    if !c.isMember() {
        c.sendError("unauthorized", "pending connections cannot list members", replyTo)
        return
    }
    // existing channel lookup and response
}
```

Do not block `sendStatus`.

- [ ] **Step 8: Run lifecycle and permission suites**

Run:

```bash
cd relay
go test ./internal/channel ./internal/ws -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

```bash
git add relay/internal/channel/registry.go relay/internal/channel/registry_test.go relay/internal/ws/hub.go relay/internal/ws/hub_test.go
git commit -m "fix: expire ownerless intercom channels"
```

---

### Task 2: Add relay acknowledgements

**Files:**

- Modify: `relay/internal/ws/hub_test.go`
- Modify: `relay/internal/ws/hub.go`
- Modify: `relay/internal/integration/e2e_test.go`

**Acceptance criteria:**

- Direct message operations produce `message.ack` only after target write succeeds.
- Join decisions produce `join.decision.ack` after registry mutation.
- Errors remain correlated and no success ACK accompanies failure.

- [ ] **Step 1: Add the ACK assertion helper and failing message ACK assertions**

First add this compiling test helper to `relay/internal/ws/hub_test.go` and `relay/internal/integration/e2e_test.go` (the packages differ, so each test package needs its own copy):

```go
func assertAck(t *testing.T, event protocol.Event, eventType, replyTo, deviceID string) {
    t.Helper()
    if event.Type != eventType || event.ReplyTo != replyTo {
        t.Fatalf("ack = type %q replyTo %q, want %q/%q", event.Type, event.ReplyTo, eventType, replyTo)
    }
    if got := stringPayload(t, event.Payload, "deviceId"); got != deviceID {
        t.Fatalf("ack device id = %q, want %q", got, deviceID)
    }
}
```

In `TestWebSocketJoinApprovalAndMessageRouting`, after each target receives `message.send`, `message.ask`, or `message.reply`, read the sender connection for:

```go
ack := readEventOfType(t, senderWS, "message.ack", func(event protocol.Event) bool {
    return event.ReplyTo == "send-1"
})
if got := stringPayload(t, ack.Payload, "status"); got != "delivered" {
    t.Fatalf("ack status = %q, want delivered", got)
}
if got := stringPayload(t, ack.Payload, "deviceId"); got != "dev_bob" {
    t.Fatalf("ack deviceId = %q, want dev_bob", got)
}
```

Add an unknown-target assertion that the sender receives correlated `unknown_target` and receives no `message.ack` for that request ID.

- [ ] **Step 2: Add failing join decision ACK assertions**

After owner approval and denial writes, assert the owner receives:

```go
ack := readEventOfType(t, aliceWS, "join.decision.ack", func(event protocol.Event) bool {
    return event.ReplyTo == "approve-1"
})
if got := stringPayload(t, ack.Payload, "decision"); got != "approved" {
    t.Fatalf("decision = %q, want approved", got)
}
```

Also assert `joinRequestId` and target `deviceId`. Add the equivalent `denied` assertion.

In `relay/internal/integration/e2e_test.go`, import `strings` and add the burst regression before implementing ACKs:

```go
func TestRemoteIntercomFiveMessageBurst(t *testing.T) {
    fixture := newRelayFixture(t)
    alice := fixture.connect(t, protocol.ConnectRequest{ChannelName: "burst", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
    aliceWS := dialMemberWS(t, alice.WSURL, alice.Token)
    bob := fixture.connect(t, protocol.ConnectRequest{ChannelName: "burst", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
    bobWS := dialMemberWS(t, bob.WSURL, bob.Token)
    _ = readUntil(t, aliceWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_bob" })

    writeEvent(t, aliceWS, protocol.Event{
        ID: "approve-burst", Type: "join.approve",
        Payload: map[string]any{"joinRequestId": bob.JoinRequestID},
    })
    _ = readUntil(t, bobWS, "join.approved", func(event protocol.Event) bool { return event.ReplyTo == "approve-burst" })
    decisionAck := readUntil(t, aliceWS, "join.decision.ack", func(event protocol.Event) bool { return event.ReplyTo == "approve-burst" })
    assertAck(t, decisionAck, "join.decision.ack", "approve-burst", "dev_bob")

    for i := 1; i <= 5; i++ {
        id := fmt.Sprintf("chunk-%d", i)
        text := fmt.Sprintf("CHUNK %d/5 ", i) + strings.Repeat(fmt.Sprintf("%d", i), 12_000)
        writeEvent(t, aliceWS, protocol.Event{
            ID: id, Type: "message.send", To: "dev_bob",
            Payload: map[string]any{"kind": "send", "text": text},
        })
        received := readUntil(t, bobWS, "message.send", func(event protocol.Event) bool { return event.ID == id })
        assertMessage(t, received, "message.send", alice.ChannelID, "dev_alice", "dev_bob", "send", text, "")
        ack := readUntil(t, aliceWS, "message.ack", func(event protocol.Event) bool { return event.ReplyTo == id })
        assertAck(t, ack, "message.ack", id, "dev_bob")
    }
}
```

- [ ] **Step 3: Run ACK tests and verify RED**

Run:

```bash
cd relay
go test ./internal/ws -run 'TestWebSocketJoinApprovalAndMessageRouting|TestDeniedPendingConnectionReceivesDeniedThenCloses' -count=1
```

Expected: FAIL while waiting for `message.ack` or `join.decision.ack`.

- [ ] **Step 4: Emit message ACK after target write**

In `handleMessageSend`, preserve the existing normalized route, then use this terminal branch:

```go
if err := target.writeEvent(event); err != nil {
    c.sendError("target_unreachable", "target is not reachable", event.ID)
    return
}
_ = c.writeEvent(protocol.Event{
    Type:      "message.ack",
    ChannelID: c.channelID,
    To:        c.deviceID,
    ReplyTo:   event.ID,
    Payload: map[string]any{
        "status":   "delivered",
        "deviceId": target.deviceID,
    },
})
```

Keep missing/offline target as `unknown_target`.

- [ ] **Step 5: Emit join decision ACK after registry mutation**

In `handleJoinDecision`, immediately after `ApproveByOwner`/`DenyByOwner` succeeds, send:

```go
decision := "denied"
if approve {
    decision = "approved"
}
_ = c.writeEvent(protocol.Event{
    Type:      "join.decision.ack",
    ChannelID: c.channelID,
    To:        c.deviceID,
    ReplyTo:   event.ID,
    Payload: map[string]any{
        "joinRequestId": joinRequestID,
        "deviceId":      join.DeviceID,
        "decision":      decision,
    },
})
``` ACK the applied registry decision even when the pending socket disappeared; notification to the pending device remains best effort after the state mutation.

- [ ] **Step 6: Run hub ACK tests and verify GREEN**

Run:

```bash
cd relay
go test ./internal/ws -count=1
```

Expected: PASS.

- [ ] **Step 7: Update end-to-end ordering for ACKs**

In `relay/internal/integration/e2e_test.go`:

- consume and validate `join.decision.ack` on Alice after Bob receives `join.approved`;
- consume and validate `message.ack` on the sender after each message receive;
- add `message.ack` to intermediary types only where event ordering can legitimately interleave;
- keep strict failure on unrecognized events.

Reuse the `assertAck` helper added in Step 1.

- [ ] **Step 8: Run relay integration and full relay suites**

```bash
cd relay
go test ./internal/integration -count=1
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 2**

```bash
git add relay/internal/ws/hub.go relay/internal/ws/hub_test.go relay/internal/integration/e2e_test.go
git commit -m "feat: acknowledge relay operations"
```

---

### Task 3: Await message ACKs and reject oversized frames in the client

**Files:**

- Modify: `extension/src/client/protocol.ts`
- Modify: `extension/src/client/relay-client.test.ts`
- Modify: `extension/src/client/relay-client.ts`

**Acceptance criteria:**

- Message methods resolve only on correlated `message.ack`.
- Correlated relay errors reject requests with their relay code.
- ACK timeout reports `delivery_unknown` and sends once.
- Complete serialized UTF-8 frames above 65,536 bytes are rejected before socket write.

- [ ] **Step 1: Define ACK protocol types**

Add to `RelayEventType`:

```ts
MessageAck: "message.ack",
JoinDecisionAck: "join.decision.ack",
```

Add payload types:

```ts
export interface MessageAckPayload extends Record<string, unknown> {
  status: "delivered";
  deviceId: string;
}

export interface JoinDecisionAckPayload extends Record<string, unknown> {
  joinRequestId: string;
  deviceId: string;
  decision: "approved" | "denied";
}
```

- [ ] **Step 2: Add failing frame-size tests**

In `relay-client.test.ts`, add:

```ts
it("rejects an oversized serialized UTF-8 frame before socket write", async () => {
  const client = new RelayClient(
    { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
    { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
  );
  await connectAndOpen(client);
  const socket = MockWebSocket.instances[0];
  const writesBefore = socket?.sent.length;

  await expect(client.send("dev_2", "한".repeat(30_000))).rejects.toMatchObject({
    code: "message_too_large",
    details: expect.objectContaining({ limitBytes: 65_536 }),
  });
  expect(socket?.sent).toHaveLength(writesBefore ?? 0);
});

it("allows a complete serialized frame at exactly 65536 bytes", async () => {
  const client = new RelayClient(
    { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
    { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
  );
  await connectAndOpen(client);
  const socket = MockWebSocket.instances[0];
  const emptyEvent = {
    id: "msg_1", type: RelayEventType.MessageSend, channelId: "ch_1", to: "dev_2",
    payload: { text: "", kind: "send" },
  };
  const overhead = Buffer.byteLength(JSON.stringify(emptyEvent), "utf8");
  const sendPromise = client.send("dev_2", "a".repeat(65_536 - overhead));
  expect(Buffer.byteLength(socket?.sent.at(-1) ?? "", "utf8")).toBe(65_536);
  socket?.emitMessage({
    type: RelayEventType.MessageAck,
    replyTo: "msg_1",
    payload: { status: "delivered", deviceId: "dev_2" },
  });
  await expect(sendPromise).resolves.toEqual(expect.objectContaining({ payload: { status: "delivered", deviceId: "dev_2" } }));
});
```

Use the deterministic ID so envelope overhead is stable.

- [ ] **Step 3: Add failing ACK, error, and timeout tests**

Add:

```ts
it("resolves send only after its correlated message ack", async () => {
  const client = new RelayClient(
    { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
    { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
  );
  await connectAndOpen(client);
  const settled = vi.fn();
  const sendPromise = client.send("dev_2", "hello");
  void sendPromise.then(settled);
  await flushPromises();
  expect(settled).not.toHaveBeenCalled();

  MockWebSocket.instances[0]?.emitMessage({
    type: RelayEventType.MessageAck,
    replyTo: "msg_1",
    payload: { status: "delivered", deviceId: "dev_2" },
  });

  await expect(sendPromise).resolves.toEqual(expect.objectContaining({
    requestEvent: expect.objectContaining({ type: RelayEventType.MessageSend }),
    responseEvent: expect.objectContaining({ type: RelayEventType.MessageAck, replyTo: "msg_1" }),
    payload: { status: "delivered", deviceId: "dev_2" },
  }));
});

it("rejects send on a correlated relay error", async () => {
  const client = new RelayClient(
    { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
    { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
  );
  await connectAndOpen(client);
  const sendPromise = client.send("missing", "hello");
  MockWebSocket.instances[0]?.emitMessage({
    type: RelayEventType.Error,
    replyTo: "msg_1",
    payload: { code: "unknown_target", message: "target is not online" },
  });
  await expect(sendPromise).rejects.toMatchObject({ code: "unknown_target" });
});

it("reports delivery unknown without replaying after ack timeout", async () => {
  vi.useFakeTimers();
  try {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];
    const sendPromise = client.send("dev_2", "hello", 5);
    expect(socket?.sent).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(5);
    await expect(sendPromise).rejects.toMatchObject({ code: "delivery_unknown" });
    expect(socket?.sent).toHaveLength(1);
  } finally {
    vi.useRealTimers();
  }
});
```

- [ ] **Step 4: Run client tests and verify RED**

```bash
cd extension
npm test -- src/client/relay-client.test.ts -t 'message ack|oversized|delivery unknown'
```

Expected: FAIL because message methods return synchronously, errors do not reject waiters, and frames are not measured.

- [ ] **Step 5: Add structured client errors and byte validation**

Extend `RelayClientError` without changing existing call sites:

```ts
export class RelayClientError extends Error {
  readonly status?: number;
  readonly details?: unknown;
  readonly code?: string;

  constructor(message: string, status?: number, details?: unknown, code?: string) {
    super(message);
    this.name = "RelayClientError";
    this.status = status;
    this.details = details;
    this.code = code;
  }
}
```

In `sendEvent`, serialize once and validate:

```ts
const serialized = JSON.stringify(outbound);
const actualBytes = Buffer.byteLength(serialized, "utf8");
if (actualBytes > 64 * 1024) {
  throw new RelayClientError(
    `relay event exceeds maximum size of ${64 * 1024} bytes`,
    undefined,
    { limitBytes: 64 * 1024, actualBytes },
    "message_too_large",
  );
}
socket.send(serialized);
```

- [ ] **Step 6: Make direct message methods await ACK**

Change `send`, `ask`, and `reply` to accept an optional `timeoutMs = 10_000`, return `Promise<RelayRequestResponse<MessageAckPayload>>`, and call the existing request/response machinery with `RelayEventType.MessageAck` and timeout code `delivery_unknown`.

Extend the waiter record with `timeoutCode`. In `resolveResponseWaiter`, reject any matching correlated `error` before checking expected response type:

```ts
if (event.type === RelayEventType.Error) {
  const payload = event.payload;
  const code = typeof payload?.code === "string" ? payload.code : "relay_error";
  const message = typeof payload?.message === "string" ? payload.message : code;
  waiter.reject(new RelayClientError(message, undefined, event, code));
  return;
}
```

Delete the waiter and clear its timer before resolve or reject.

- [ ] **Step 7: Run focused client tests and verify GREEN**

```bash
cd extension
npm test -- src/client/relay-client.test.ts -t 'message ack|oversized|delivery unknown'
```

Expected: PASS.

- [ ] **Step 8: Update existing serialization tests for async methods**

Store message promises, inspect outbound serialization, emit correlated ACK events, and await each promise. Do not weaken event-shape assertions.

- [ ] **Step 9: Run the full client suite**

```bash
cd extension
npm test -- src/client/relay-client.test.ts
```

Expected: PASS.

- [ ] **Step 10: Commit Task 3**

```bash
git add extension/src/client/protocol.ts extension/src/client/relay-client.ts extension/src/client/relay-client.test.ts
git commit -m "feat: confirm intercom message delivery"
```

---

### Task 4: Make tool results acknowledgement-aware

**Files:**

- Modify: `extension/src/tools/intercom-tool.test.ts`
- Modify: `extension/src/tools/intercom-tool.ts`

**Acceptance criteria:**

- Message tool results include request and ACK events.
- Join decisions delete local pending state only after decision ACK.
- Decision error/timeout retains pending state.

- [ ] **Step 1: Make the mock client return acknowledged operations**

Change mock `send`, `ask`, `reply`, `approveJoin`, and `denyJoin` methods to async `RelayRequestResponse` results. Use a helper that records the request and returns a matching response:

```ts
private acknowledge<TPayload extends Record<string, unknown>>(
  requestEvent: RelayEvent,
  responseType: string,
  payload: TPayload,
) {
  return Promise.resolve({
    requestEvent,
    responseEvent: { type: responseType, replyTo: requestEvent.id, payload },
    payload,
  });
}
```

- [ ] **Step 2: Add failing pending-state tests**

Add a local deferred helper:

```ts
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
```

Add the ACK-gated deletion test:

```ts
it("keeps a join pending until the relay acknowledges approval", async () => {
  const client = new MockClient();
  const pending = new PendingState();
  pending.addJoinRequest({
    id: "join_1", joinRequestId: "join_1", deviceId: "dev_2",
    deviceName: "Laptop", channelId: "ch_1", receivedAt: "2026-01-01T00:00:00.000Z",
  });
  const decision = deferred<Awaited<ReturnType<MockClient["approveJoin"]>>>();
  vi.spyOn(client, "approveJoin").mockReturnValue(decision.promise);
  const tool = createRemoteIntercomTool({ client, pending });

  const execution = tool.execute({ action: "approve_join", joinRequestId: "join_1" });
  expect(pending.getJoinRequest("join_1")).toBeDefined();
  decision.resolve({
    requestEvent: { id: "evt_approve", type: RelayEventType.JoinApprove },
    responseEvent: {
      type: RelayEventType.JoinDecisionAck, replyTo: "evt_approve",
      payload: { joinRequestId: "join_1", deviceId: "dev_2", decision: "approved" },
    },
    payload: { joinRequestId: "join_1", deviceId: "dev_2", decision: "approved" },
  });

  await expect(execution).resolves.toEqual(expect.objectContaining({
    action: "approve_join",
    responseEvent: expect.objectContaining({ type: RelayEventType.JoinDecisionAck }),
  }));
  expect(pending.getJoinRequest("join_1")).toBeUndefined();
});
```

Add a rejection test using `vi.spyOn(client, "approveJoin").mockRejectedValue(Object.assign(new Error("only the owner can approve"), { code: "unauthorized" }))`, await rejection, and assert `pending.getJoinRequest("join_1")` remains. Repeat with code `decision_unknown`.

- [ ] **Step 3: Run tool tests and verify RED**

```bash
cd extension
npm test -- src/tools/intercom-tool.test.ts -t 'ack|pending state'
```

Expected: FAIL because the tool deletes pending immediately and message results do not include ACKs.

- [ ] **Step 4: Update client interface and tool result types**

Use the protocol ACK payloads in `RemoteIntercomClient`. Change message and decision methods to promises. Change success result variants to include both:

```ts
{
  ok: true;
  action: "send" | "ask" | "reply" | "approve_join" | "deny_join";
  event: RelayEvent;
  responseEvent: RelayEvent;
  connection: RelayClientState;
}
```

- [ ] **Step 5: Await ACK before mutating local state**

For messages, await the client result and return its request and response events.

For decisions:

```ts
const response = await context.client.approveJoin(joinRequestId);
context.pending.deleteJoinRequest(joinRequestId);
return {
  ok: true,
  action: "approve_join",
  event: response.requestEvent,
  responseEvent: response.responseEvent,
  connection: connectionState(context.client),
};
```

Do not catch decision errors merely to delete state; let them reject while pending remains.

- [ ] **Step 6: Run focused and full tool suites**

```bash
cd extension
npm test -- src/tools/intercom-tool.test.ts
npm test
```

Expected: PASS.

- [ ] **Step 7: Commit Task 4**

```bash
git add extension/src/tools/intercom-tool.ts extension/src/tools/intercom-tool.test.ts
git commit -m "fix: await intercom operation acknowledgements"
```

---

### Task 5: Detect idle half-open connections and reconnect single-flight

**Files:**

- Modify: `extension/src/client/relay-client.test.ts`
- Modify: `extension/src/client/relay-client.ts`
- Modify: `extension/src/tools/intercom-tool.test.ts`
- Modify: `extension/src/tools/intercom-tool.ts`

**Acceptance criteria:**

- A locally open socket that stops answering status heartbeats becomes stale within configured test timing and reconnects.
- Members and pending devices reconnect.
- Concurrent heartbeat/tool recovery opens one replacement socket.
- Explicit disconnect stops heartbeat and reconnect.
- Tool actions preflight stale transport and execute once.

- [ ] **Step 1: Add heartbeat dependency controls and failing half-open test**

Extend test dependencies with `heartbeatIntervalMs` and `heartbeatTimeoutMs`. In a fake-timer test:

1. connect a client with `heartbeatIntervalMs: 20`, `heartbeatTimeoutMs: 5`, and `reconnectDelayMs: 5`;
2. leave the first `MockWebSocket.readyState` open but emit no response;
3. advance 25 ms;
4. assert diagnostics include `heartbeat_timeout` and `reconnect_scheduled`;
5. advance reconnect delay and assert a second fetch/socket exists.

Expected RED: no heartbeat request or reconnect exists.

- [ ] **Step 2: Add failing pending and disconnect tests**

Add one test with connect status `pending_approval` proving heartbeat timeout reconnects using the same device ID and pending join. Add one test that explicitly disconnects, advances several heartbeat intervals, and asserts no fetch/socket was added.

- [ ] **Step 3: Add failing single-flight recovery test**

Let heartbeat timeout and two network operations encounter stale transport together. Assert only one reconnect fetch and one replacement socket. After opening the replacement socket, emit responses and prove each operation executes once.

- [ ] **Step 4: Run heartbeat tests and verify RED**

```bash
cd extension
npm test -- src/client/relay-client.test.ts -t 'heartbeat|half-open|single reconnect|pending connection'
```

Expected: FAIL because the current client reconnects only after close events and excludes pending status.

- [ ] **Step 5: Add transport state and heartbeat timer**

Extend `RelayClientDependencies`:

```ts
heartbeatIntervalMs?: number;
heartbeatTimeoutMs?: number;
```

Extend `RelayClientState` with optional diagnostics:

```ts
transportStatus?: "connected" | "stale" | "reconnecting" | "disconnected";
lastRelayActivityAt?: string;
lastDisconnectedAt?: string;
lastCloseCode?: number;
lastCloseReason?: string;
reconnectAttempt?: number;
```

Add one interval handle, one in-flight heartbeat flag, and one reconnect promise. Start the timer after socket open; clear it before socket replacement and explicit disconnect. Call `.unref?.()` on the Node timer.

- [ ] **Step 6: Record activity and make pending state reconnectable**

On socket open and every parsed inbound event, update `lastRelayActivityAt` and set `transportStatus: "connected"`.

Replace `isEstablishedMemberConnection` with:

```ts
private isReconnectableConnection(): boolean {
  return this.state.status === "created"
    || this.state.status === "connected"
    || this.state.status === "pending_approval";
}
```

Use it in close and heartbeat recovery.

- [ ] **Step 7: Implement heartbeat timeout handling**

On each interval, send a low-level correlated `status.request` with the configured 5-second timeout. Do not call the public `status()` method from heartbeat, because public status performs health preflight.

On timeout:

```ts
this.state.transportStatus = "stale";
this.emitError("heartbeat_timeout", error, {
  deviceId: this.state.deviceId,
  channelId: this.state.channelId,
});
this.closeCurrentSocket(undefined, undefined, true);
this.scheduleReconnect();
```

Ignore overlapping ticks while one heartbeat is in flight.

- [ ] **Step 8: Make reconnect single-flight and cancelable**

Store the active reconnect promise and return it to all callers. Clear it in `finally`. Add a monotonically increasing connection generation: explicit `connect` and `disconnect` increment it; an HTTP/socket continuation whose captured generation is stale must close/cancel instead of reopening transport.

`disconnect` must:

- set `explicitDisconnect`;
- increment generation;
- clear heartbeat and reconnect timers;
- close the current socket with reconnect suppressed;
- set `transportStatus: "disconnected"`.

- [ ] **Step 9: Add healthy-connection preflight**

Add `ensureHealthyConnection()`:

- return when socket is open and recent heartbeat/activity is within `heartbeatIntervalMs + heartbeatTimeoutMs`;
- if open but stale, perform one low-level status probe;
- on probe failure, close stale transport and await the shared reconnect promise;
- if closed and logically reconnectable, await shared reconnect;
- otherwise throw `relay WebSocket is not connected`.

Call it before public `list`, `status`, message, and decision operations. Do not call it from `connect`, `disconnect`, or local `pending` tool behavior.

- [ ] **Step 10: Run heartbeat tests and verify GREEN**

```bash
cd extension
npm test -- src/client/relay-client.test.ts -t 'heartbeat|half-open|single reconnect|pending connection'
```

Expected: PASS.

- [ ] **Step 11: Prove tool actions use client preflight without reconnecting local pending**

Update tool mock/tests to expose a stale `connectState` and verify network actions await the client operation, while `{ action: "pending" }` performs no network call. The actual preflight remains encapsulated in `RelayClient`; the tool does not duplicate liveness logic.

- [ ] **Step 12: Run all extension tests and bundle build**

```bash
cd extension
npm test
npm run build:bundle
```

Expected: PASS with no TypeScript errors.

- [ ] **Step 13: Commit Task 5**

```bash
git add extension/src/client/relay-client.ts extension/src/client/relay-client.test.ts extension/src/tools/intercom-tool.ts extension/src/tools/intercom-tool.test.ts
git commit -m "fix: recover idle intercom connections"
```

---

### Task 6: Verify the complete contract

**Files:**

- Modify: `relay/internal/integration/e2e_test.go`
- Review/update only if required: `AGENTS.md`
- Review/update only if required: `docs/concept.md`
- Review/update only if required: `protocol/wire.md`
- Review/update only if required: `docs/specs/*.md`

**Acceptance criteria:**

- Five approximately 12 KiB messages each reach the target and produce a unique ACK.
- All spec criteria map to passing tests.
- Documentation matches final event names, limits, timing, and error codes.

- [ ] **Step 1: Run the five-message integration regression added RED-first in Task 2**

Run:

```bash
cd relay
go test ./internal/integration -run TestRemoteIntercomFiveMessageBurst -count=1
```

Expected: PASS with five target receives and five correlated ACKs.

- [ ] **Step 2: Run every required verification command**

```bash
cd relay && go test ./... -count=1
cd extension && npm test
cd extension && npm run build:bundle
```

Expected: all commands exit 0.

- [ ] **Step 3: Check spec and documentation consistency**

Run:

```bash
rg -n 'message\.ack|join\.decision\.ack|65,536|64 KiB|25 seconds|5 seconds|10 seconds|automatic.*replay' \
  AGENTS.md docs/decisions docs/specs docs/concept.md protocol/wire.md
git diff --check
```

Confirm implementation constants and event names match the documents. Change documentation only when the implementation proves an accepted spec statement was technically impossible; do not silently change product decisions.

- [ ] **Step 4: Run a Ponytail scope audit**

Confirm the diff contains:

- no new dependency;
- no datastore;
- no automatic chunking;
- no automatic message replay;
- one heartbeat timer and one reconnect promise;
- targeted ACK types rather than a generic command bus.

- [ ] **Step 5: Commit final integration or documentation adjustments**

```bash
git add relay/internal/integration/e2e_test.go AGENTS.md docs protocol/wire.md
git commit -m "test: cover reliable intercom delivery"
```

If no files changed after prior commits, skip this commit rather than creating an empty commit.

---

## Checkpoints

### Checkpoint A — after Tasks 1–2

- [ ] Relay lifecycle matches ADR-0001.
- [ ] Pending permissions are enforced.
- [ ] Relay emits correlated ACK/error events.
- [ ] `cd relay && go test ./... -count=1` passes.

### Checkpoint B — after Tasks 3–4

- [ ] Client rejects oversized frames before write.
- [ ] Message and decision tools await ACK.
- [ ] Unknown outcomes do not replay.
- [ ] `cd extension && npm test` passes.

### Checkpoint C — after Task 5

- [ ] Half-open member and pending sockets recover.
- [ ] Concurrent recovery is single-flight.
- [ ] Explicit disconnect stays disconnected.
- [ ] Extension tests and bundle build pass.

### Final checkpoint — after Task 6

- [ ] All three specs' success criteria have executable evidence.
- [ ] Relay and extension full suites pass.
- [ ] Documentation and agent rules match implementation.
- [ ] Diff contains no out-of-scope architecture.

## Risks and mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| ACK changes event ordering in existing end-to-end tests | Medium | Consume ACK explicitly; keep strict unexpected-event failures. |
| ACK timeout races relay write timeout | High | Use 10-second ACK/decision timeout over the relay's 5-second write bound. |
| Heartbeat calls public status and recurses through health preflight | High | Separate low-level correlated status request from public preflighted `status()`. |
| Heartbeat and close event open competing reconnects | High | One shared reconnect promise and timer. |
| Explicit disconnect races an in-flight reconnect | High | Connection generation invalidates stale async continuation. |
| Automatic resend duplicates side effects | High | Never replay after post-write ACK timeout. |
| Pending list restriction breaks undocumented behavior | Low | This is an accepted security contract with explicit status access retained. |
| Plan grows into durable messaging | Medium | Enforce ADR and Ponytail scope audit; no DB, queue, chunking, or processing receipt. |

## Open questions

None. Specs and architecture were approved on 2026-07-19.
