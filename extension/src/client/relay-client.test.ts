import { beforeEach, describe, expect, it, vi } from "vitest";

import { RelayClient, type FetchLike, type FetchResponseLike, type WebSocketLike } from "./relay-client.js";
import { RelayEventType, type ConnectResponse, type RelayEvent } from "./protocol.js";

type MockWebSocketEvent = "open" | "message" | "close" | "error";

class MockWebSocket implements WebSocketLike {
  static instances: MockWebSocket[] = [];

  readonly url: string;
  readyState = 0;
  sent: string[] = [];
  private handlers = new Map<MockWebSocketEvent, Set<(...args: unknown[]) => void>>();

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code = 1000, reason = ""): void {
    this.readyState = 3;
    this.emit("close", { code, reason, wasClean: code === 1000 });
  }

  on(event: MockWebSocketEvent, handler: (...args: unknown[]) => void): void {
    const handlers = this.handlers.get(event) ?? new Set<(...args: unknown[]) => void>();
    handlers.add(handler);
    this.handlers.set(event, handlers);
  }

  off(event: MockWebSocketEvent, handler: (...args: unknown[]) => void): void {
    this.handlers.get(event)?.delete(handler);
  }

  removeListener(event: MockWebSocketEvent, handler: (...args: unknown[]) => void): void {
    this.off(event, handler);
  }

  emitOpen(): void {
    this.readyState = 1;
    this.emit("open");
  }

  emitError(error: unknown): void {
    this.emit("error", error);
  }

  emitMessage(event: RelayEvent): void {
    this.emit("message", JSON.stringify(event));
  }

  private emit(event: MockWebSocketEvent, ...args: unknown[]): void {
    for (const handler of [...(this.handlers.get(event) ?? [])]) {
      handler(...args);
    }
  }
}

function resetSockets(): void {
  MockWebSocket.instances = [];
}

async function flushPromises(): Promise<void> {
  for (let index = 0; index < 5; index += 1) {
    await Promise.resolve();
  }
}

async function connectAndOpen(client: RelayClient, channel = "dwkim", pin = "1234"): Promise<ConnectResponse> {
  const connectPromise = client.connect(channel, pin);
  await flushPromises();
  const socket = MockWebSocket.instances.at(-1);
  expect(socket).toBeDefined();
  socket?.emitOpen();
  return connectPromise;
}

function connectPayload(overrides: Partial<ConnectResponse> = {}): ConnectResponse {
  return {
    status: "created",
    channelId: "ch_1",
    deviceId: "dev_1",
    token: "token with spaces",
    wsUrl: "ws://relay.example/ws",
    ...overrides,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function mockFetch(payload: unknown, init: { ok?: boolean; status?: number; statusText?: string } = {}): FetchLike {
  return vi.fn(async () => ({
    ok: init.ok ?? true,
    status: init.status ?? 200,
    statusText: init.statusText,
    json: async () => payload,
  }));
}

describe("RelayClient", () => {
  beforeEach(() => {
    resetSockets();
  });

  it("posts connect body and opens WebSocket with token query after the socket opens", async () => {
    const fetchImpl = mockFetch(connectPayload());
    const client = new RelayClient(
      {
        relayHttpUrl: "http://relay.example/",
        deviceName: "test-device",
        deviceId: "dev_configured",
        clientVersion: "1.2.3",
      },
      { fetch: fetchImpl, WebSocket: MockWebSocket },
    );

    const connectPromise = client.connect("dwkim", "1234");
    const resolved = vi.fn();
    connectPromise.then(resolved);
    await flushPromises();

    expect(resolved).not.toHaveBeenCalled();
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(MockWebSocket.instances[0]?.readyState).toBe(0);
    expect(MockWebSocket.instances[0]?.url).toBe("ws://relay.example/ws?token=token+with+spaces");

    MockWebSocket.instances[0]?.emitOpen();
    const response = await connectPromise;

    expect(response.token).toBe("token with spaces");
    expect(resolved).toHaveBeenCalledTimes(1);
    expect(client.token).toBe("token with spaces");
    expect(client.channelId).toBe("ch_1");
    expect(fetchImpl).toHaveBeenCalledWith("http://relay.example/channels/connect", {
      method: "POST",
      headers: {
        accept: "application/json",
        "content-type": "application/json",
      },
      body: JSON.stringify({
        channelName: "dwkim",
        pin: "1234",
        deviceName: "test-device",
        deviceId: "dev_configured",
        clientVersion: "1.2.3",
      }),
      signal: undefined,
    });
  });

  it("rejects connect when the WebSocket errors before opening", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );

    const connectPromise = client.connect("dwkim", "1234");
    const rejection = expect(connectPromise).rejects.toMatchObject({
      message: "relay WebSocket error before opening: boom",
      details: new Error("boom"),
    });
    await flushPromises();
    MockWebSocket.instances[0]?.emitError(new Error("boom"));

    await rejection;
    expect(client.webSocket).toBeUndefined();
  });

  it("emits error events when the WebSocket errors after opening", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const errorHandler = vi.fn();
    client.on(RelayEventType.Error, errorHandler);

    await connectAndOpen(client);
    MockWebSocket.instances[0]?.emitError(new Error("late boom"));

    expect(errorHandler).toHaveBeenCalledTimes(1);
    expect(errorHandler).toHaveBeenCalledWith(expect.objectContaining({
      type: RelayEventType.Error,
      channelId: "ch_1",
      payload: expect.objectContaining({ code: "websocket_error", message: "late boom" }),
    }));
  });

  it("invokes registered handlers for incoming WebSocket messages", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const handler = vi.fn();
    client.on(RelayEventType.MessageSend, handler);

    await connectAndOpen(client);
    MockWebSocket.instances[0]?.emitMessage({
      id: "evt_in",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "hello" },
    });

    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ id: "evt_in", from: "dev_2" }));
  });

  it("waits for list and status responses that match the request id", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "req_1" },
    );

    await connectAndOpen(client);
    const listPromise = client.list();
    expect(JSON.parse(MockWebSocket.instances[0]?.sent.at(-1) ?? "{}")).toEqual(expect.objectContaining({
      id: "req_1",
      type: RelayEventType.ListRequest,
    }));

    MockWebSocket.instances[0]?.emitMessage({
      id: "list_resp_1",
      type: RelayEventType.ListResponse,
      channelId: "ch_1",
      replyTo: "req_1",
      payload: {
        ownerId: "dev_1",
        members: [
          { deviceId: "dev_1", deviceName: "Alice", online: true, owner: true },
          { deviceId: "dev_2", deviceName: "Bob", online: true, owner: false },
        ],
      },
    });

    await expect(listPromise).resolves.toEqual(expect.objectContaining({
      requestEvent: expect.objectContaining({ id: "req_1", type: RelayEventType.ListRequest }),
      responseEvent: expect.objectContaining({ type: RelayEventType.ListResponse, replyTo: "req_1" }),
      payload: expect.objectContaining({ members: [expect.objectContaining({ deviceName: "Alice" }), expect.objectContaining({ deviceId: "dev_2" })] }),
    }));

    const statusPromise = client.status();
    MockWebSocket.instances[0]?.emitMessage({
      id: "status_resp_1",
      type: RelayEventType.StatusResponse,
      channelId: "ch_1",
      replyTo: "req_1",
      payload: { status: "member", channelId: "ch_1", deviceId: "dev_1", ownerId: "dev_1" },
    });

    await expect(statusPromise).resolves.toEqual(expect.objectContaining({
      responseEvent: expect.objectContaining({ type: RelayEventType.StatusResponse }),
      payload: expect.objectContaining({ status: "member", deviceId: "dev_1" }),
    }));
  });

  it("updates pending connection state when join is approved", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload({ status: "pending_approval", token: "pending-token", joinRequestId: "join_1" })), WebSocket: MockWebSocket },
    );

    await connectAndOpen(client);
    expect(client.currentStatus).toBe("pending_approval");
    expect(client.token).toBe("pending-token");

    MockWebSocket.instances[0]?.emitMessage({
      type: RelayEventType.JoinApproved,
      channelId: "ch_1",
      payload: { joinRequestId: "join_1", deviceId: "dev_1", token: "member-token" },
    });

    expect(client.currentStatus).toBe("connected");
    expect(client.token).toBe("member-token");
    expect(client.connectState.joinRequestId).toBeUndefined();
  });

  it("ignores messages from sockets after disconnect", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const handler = vi.fn();
    client.on(RelayEventType.MessageSend, handler);

    await connectAndOpen(client);
    const oldSocket = MockWebSocket.instances[0];
    client.disconnect();
    oldSocket?.emitMessage({
      id: "stale",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "stale" },
    });

    expect(handler).not.toHaveBeenCalled();
  });

  it("ignores messages from sockets after reconnect", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const handler = vi.fn();
    client.on(RelayEventType.MessageSend, handler);

    await connectAndOpen(client);
    const oldSocket = MockWebSocket.instances[0];
    const reconnectPromise = client.connect("dwkim", "1234");
    await flushPromises();
    const newSocket = MockWebSocket.instances[1];
    expect(newSocket).toBeDefined();

    oldSocket?.emitMessage({
      id: "old_before_open",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "old before open" },
    });
    newSocket?.emitOpen();
    await reconnectPromise;
    oldSocket?.emitMessage({
      id: "old_after_open",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "old after open" },
    });
    newSocket?.emitMessage({
      id: "new_after_open",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "new after open" },
    });

    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ id: "new_after_open" }));
  });

  it("continues dispatching later handlers when an earlier handler throws", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const thrown = new Error("handler failed");
    const secondHandler = vi.fn();
    const errorHandler = vi.fn();
    client.on(RelayEventType.MessageSend, () => {
      throw thrown;
    });
    client.on(RelayEventType.MessageSend, secondHandler);
    client.on(RelayEventType.Error, errorHandler);

    await connectAndOpen(client);
    MockWebSocket.instances[0]?.emitMessage({
      id: "evt_in",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_1",
      payload: { text: "hello" },
    });

    expect(secondHandler).toHaveBeenCalledTimes(1);
    expect(errorHandler).toHaveBeenCalledTimes(1);
    expect(errorHandler).toHaveBeenCalledWith(expect.objectContaining({
      type: RelayEventType.Error,
      payload: expect.objectContaining({
        code: "handler_error",
        message: "handler failed",
        handlerEventType: RelayEventType.MessageSend,
      }),
    }));
  });

  it("returns frame validation failures as rejected promises", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);

    let operation: ReturnType<RelayClient["send"]> | undefined;
    expect(() => {
      operation = client.send("dev_2", "한".repeat(30_000));
    }).not.toThrow();
    await expect(operation).rejects.toMatchObject({ code: "message_too_large" });
  });

  it("rejects an oversized serialized UTF-8 frame before socket write", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];
    const writesBefore = socket?.sent.length ?? 0;

    await expect(Promise.resolve().then(() => client.send("dev_2", "한".repeat(30_000)))).rejects.toMatchObject({
      code: "message_too_large",
      details: expect.objectContaining({ limitBytes: 65_536 }),
    });
    expect(socket?.sent).toHaveLength(writesBefore);
  });

  it("allows a complete serialized frame at exactly 65536 bytes", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];
    const emptyEvent = {
      type: RelayEventType.MessageSend,
      to: "dev_2",
      payload: { text: "", kind: "send" },
      id: "msg_1",
      channelId: "ch_1",
    };
    const overhead = Buffer.byteLength(JSON.stringify(emptyEvent), "utf8");
    const result = client.send("dev_2", "a".repeat(65_536 - overhead));
    expect(result).toBeInstanceOf(Promise);
    if (!(result instanceof Promise)) return;

    expect(Buffer.byteLength(socket?.sent.at(-1) ?? "", "utf8")).toBe(65_536);
    socket?.emitMessage({
      type: "message.ack",
      replyTo: "msg_1",
      payload: { status: "delivered", deviceId: "dev_2" },
    });
    await expect(result).resolves.toEqual(expect.objectContaining({ payload: { status: "delivered", deviceId: "dev_2" } }));
  });

  it("resolves send only after its correlated message ack", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);
    const result = client.send("dev_2", "hello");
    expect(result).toBeInstanceOf(Promise);
    if (!(result instanceof Promise)) return;

    const settled = vi.fn();
    void result.then(settled);
    await flushPromises();
    expect(settled).not.toHaveBeenCalled();
    MockWebSocket.instances[0]?.emitMessage({
      type: "message.ack",
      replyTo: "msg_1",
      payload: { status: "delivered", deviceId: "dev_2" },
    });
    await expect(result).resolves.toEqual(expect.objectContaining({
      requestEvent: expect.objectContaining({ type: RelayEventType.MessageSend }),
      responseEvent: expect.objectContaining({ type: "message.ack", replyTo: "msg_1" }),
      payload: { status: "delivered", deviceId: "dev_2" },
    }));
  });

  it("rejects send on a correlated relay error", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "msg_1" },
    );
    await connectAndOpen(client);
    const result = client.send("missing", "hello");
    expect(result).toBeInstanceOf(Promise);
    if (!(result instanceof Promise)) return;

    MockWebSocket.instances[0]?.emitMessage({
      type: RelayEventType.Error,
      replyTo: "msg_1",
      payload: { code: "unknown_target", message: "target is not online" },
    });
    await expect(result).rejects.toMatchObject({ code: "unknown_target" });
  });

  it("rejects mismatched message and join decision acknowledgements", async () => {
    let sequence = 0;
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => `evt_${++sequence}` },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];

    const sendPromise = client.send("dev_2", "hello");
    socket?.emitMessage({
      type: RelayEventType.MessageAck,
      replyTo: "evt_1",
      payload: { status: "delivered", deviceId: "" },
    });
    await expect(sendPromise).rejects.toMatchObject({ code: "invalid_response" });

    const decisionPromise = client.approve("join_1");
    socket?.emitMessage({
      type: RelayEventType.JoinDecisionAck,
      replyTo: "evt_2",
      payload: { joinRequestId: "join_1", deviceId: "dev_2", decision: "denied" },
    });
    await expect(decisionPromise).rejects.toMatchObject({ code: "invalid_response" });
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
      const result = client.send("dev_2", "hello", 5);
      expect(result).toBeInstanceOf(Promise);
      if (!(result instanceof Promise)) return;

      expect(socket?.sent).toHaveLength(1);
      const rejection = expect(result).rejects.toMatchObject({ code: "delivery_unknown" });
      await vi.advanceTimersByTimeAsync(5);
      await rejection;
      expect(socket?.sent).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("serializes send, ask, reply, join decisions, list, and status events", async () => {
    let counter = 0;
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      {
        fetch: mockFetch(connectPayload()),
        WebSocket: MockWebSocket,
        idGenerator: () => `evt_${++counter}`,
      },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];
    expect(socket).toBeDefined();

    const sendPromise = client.send("dev_2", "hello");
    const askPromise = client.ask("dev_2", "question?");
    socket?.emitMessage({ id: "ask_in", type: RelayEventType.MessageAsk, from: "dev_2", to: "dev_1", payload: { text: "remote question?", kind: "ask" } });
    const replyPromise = client.reply("ask_in", "answer");
    const approvePromise = client.approve("join_1");
    const denyPromise = client.deny("join_2");
    const listPromise = client.list();
    const statusPromise = client.status();

    const events = socket?.sent.map((raw) => JSON.parse(raw) as RelayEvent) ?? [];
    expect(events).toEqual([
      expect.objectContaining({ id: "evt_1", type: RelayEventType.MessageSend, channelId: "ch_1", to: "dev_2", payload: { text: "hello", kind: "send" } }),
      expect.objectContaining({ id: "evt_2", type: RelayEventType.MessageAsk, channelId: "ch_1", to: "dev_2", payload: { text: "question?", kind: "ask" } }),
      expect.objectContaining({ id: "evt_3", type: RelayEventType.MessageReply, channelId: "ch_1", to: "dev_2", replyTo: "ask_in", payload: { text: "answer", kind: "reply" } }),
      expect.objectContaining({ id: "evt_4", type: RelayEventType.JoinApprove, channelId: "ch_1", payload: { joinRequestId: "join_1" } }),
      expect.objectContaining({ id: "evt_5", type: RelayEventType.JoinDeny, channelId: "ch_1", payload: { joinRequestId: "join_2" } }),
      expect.objectContaining({ id: "evt_6", type: RelayEventType.ListRequest, channelId: "ch_1" }),
      expect.objectContaining({ id: "evt_7", type: RelayEventType.StatusRequest, channelId: "ch_1" }),
    ]);

    socket?.emitMessage({ type: RelayEventType.MessageAck, replyTo: "evt_1", payload: { status: "delivered", deviceId: "dev_2" } });
    socket?.emitMessage({ type: RelayEventType.MessageAck, replyTo: "evt_2", payload: { status: "delivered", deviceId: "dev_2" } });
    socket?.emitMessage({ type: RelayEventType.MessageAck, replyTo: "evt_3", payload: { status: "delivered", deviceId: "dev_2" } });
    socket?.emitMessage({ type: RelayEventType.JoinDecisionAck, replyTo: "evt_4", payload: { joinRequestId: "join_1", deviceId: "dev_2", decision: "approved" } });
    socket?.emitMessage({ type: RelayEventType.JoinDecisionAck, replyTo: "evt_5", payload: { joinRequestId: "join_2", deviceId: "dev_3", decision: "denied" } });
    socket?.emitMessage({ type: RelayEventType.ListResponse, replyTo: "evt_6", payload: { ownerId: "dev_1", members: [] } });
    socket?.emitMessage({ type: RelayEventType.StatusResponse, replyTo: "evt_7", payload: { status: "member", channelId: "ch_1", deviceId: "dev_1", ownerId: "dev_1" } });
    await Promise.all([sendPromise, askPromise, replyPromise, approvePromise, denyPromise, listPromise, statusPromise]);
  });

  it("serializes approveJoin and denyJoin aliases", async () => {
    let counter = 0;
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      {
        fetch: mockFetch(connectPayload()),
        WebSocket: MockWebSocket,
        idGenerator: () => `evt_${++counter}`,
      },
    );
    await connectAndOpen(client);
    const socket = MockWebSocket.instances[0];

    const approvePromise = client.approveJoin("join_alias_1");
    const denyPromise = client.denyJoin("join_alias_2");

    const events = socket?.sent.map((raw) => JSON.parse(raw) as RelayEvent) ?? [];
    expect(events).toEqual([
      expect.objectContaining({ id: "evt_1", type: RelayEventType.JoinApprove, channelId: "ch_1", payload: { joinRequestId: "join_alias_1" } }),
      expect.objectContaining({ id: "evt_2", type: RelayEventType.JoinDeny, channelId: "ch_1", payload: { joinRequestId: "join_alias_2" } }),
    ]);
    socket?.emitMessage({ type: RelayEventType.JoinDecisionAck, replyTo: "evt_1", payload: { joinRequestId: "join_alias_1", deviceId: "dev_2", decision: "approved" } });
    socket?.emitMessage({ type: RelayEventType.JoinDecisionAck, replyTo: "evt_2", payload: { joinRequestId: "join_alias_2", deviceId: "dev_3", decision: "denied" } });
    await Promise.all([approvePromise, denyPromise]);
  });

  it("emits diagnostic events when an established member socket closes and schedules reconnect", async () => {
    vi.useFakeTimers();
    const errorHandler = vi.fn();
    let connectCount = 0;
    const fetchImpl = vi.fn<FetchLike>(async () => {
      connectCount += 1;
      return {
        ok: true,
        status: 200,
        json: async () => connectPayload({ token: `token_${connectCount}` }),
      };
    });
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: fetchImpl, WebSocket: MockWebSocket, reconnectDelayMs: 5 },
    );
    client.on(RelayEventType.Error, errorHandler);

    try {
      await connectAndOpen(client);
      MockWebSocket.instances[0]?.close(1006, "network gone");

      expect(errorHandler).toHaveBeenCalledWith(expect.objectContaining({
        type: RelayEventType.Error,
        payload: expect.objectContaining({
          code: "websocket_close",
          closeCode: 1006,
          reason: "network gone",
          willReconnect: true,
          deviceId: "dev_1",
          channelId: "ch_1",
        }),
      }));
      expect(errorHandler).toHaveBeenCalledWith(expect.objectContaining({
        type: RelayEventType.Error,
        payload: expect.objectContaining({
          code: "reconnect_scheduled",
          attempt: 1,
          delayMs: 5,
          deviceId: "dev_1",
          channelId: "ch_1",
        }),
      }));

      await vi.advanceTimersByTimeAsync(5);
      await flushPromises();
      expect(fetchImpl).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("schedules heartbeat from the latest inbound activity", async () => {
    vi.useFakeTimers();
    try {
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => "heartbeat_1" },
      );
      await connectAndOpen(client);
      const socket = MockWebSocket.instances[0];

      await vi.advanceTimersByTimeAsync(24_000);
      socket?.emitMessage({ type: "presence.changed", payload: { deviceId: "dev_2", online: true } });
      await vi.advanceTimersByTimeAsync(24_999);
      expect(socket?.sent).toHaveLength(0);
      await vi.advanceTimersByTimeAsync(1);
      expect(JSON.parse(socket?.sent.at(-1) ?? "{}")).toEqual(expect.objectContaining({ type: RelayEventType.StatusRequest }));
      socket?.emitMessage({
        type: RelayEventType.StatusResponse,
        replyTo: "heartbeat_1",
        payload: { status: "member", channelId: "ch_1", deviceId: "dev_1", ownerId: "dev_1" },
      });
      client.disconnect();
    } finally {
      vi.useRealTimers();
    }
  });

  it("reconnects a pending half-open socket after heartbeat timeout", async () => {
    vi.useFakeTimers();
    try {
      const diagnostics: string[] = [];
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: mockFetch(connectPayload({ status: "pending_approval", joinRequestId: "join_1" })), WebSocket: MockWebSocket },
      );
      client.on(RelayEventType.Error, (event) => diagnostics.push(String(event.payload?.code)));
      await connectAndOpen(client);
      const original = MockWebSocket.instances[0];

      await vi.advanceTimersByTimeAsync(25_000);
      expect(JSON.parse(original?.sent.at(-1) ?? "{}")).toEqual(expect.objectContaining({ type: RelayEventType.StatusRequest }));
      await vi.advanceTimersByTimeAsync(5_000);
      await flushPromises();

      expect(MockWebSocket.instances).toHaveLength(1);
      expect(client.connectState).toEqual(expect.objectContaining({
        transportStatus: "reconnecting",
        lastRelayActivityAt: expect.any(String),
        lastDisconnectedAt: expect.any(String),
        reconnectAttempt: 1,
      }));
      await vi.advanceTimersByTimeAsync(249);
      expect(MockWebSocket.instances).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(1);
      await flushPromises();
      expect(MockWebSocket.instances).toHaveLength(2);
      const replacement = MockWebSocket.instances[1];
      replacement?.emitOpen();
      await flushPromises();

      expect(client.currentStatus).toBe("pending_approval");
      expect(client.connectState.transportStatus).toBe("connected");
      expect(diagnostics).toEqual(expect.arrayContaining(["heartbeat_timeout", "reconnect_attempt", "reconnect_success"]));
    } finally {
      vi.useRealTimers();
    }
  });

  it("shares one stale preflight reconnect across concurrent operations", async () => {
    vi.useFakeTimers();
    try {
      let sequence = 0;
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket, idGenerator: () => `evt_${++sequence}` },
      );
      await connectAndOpen(client);

      await vi.advanceTimersByTimeAsync(25_000);
      const sendPromise = client.send("dev_2", "hello");
      const askPromise = client.ask("dev_2", "question?");
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.advanceTimersByTimeAsync(250);
      await flushPromises();

      expect(MockWebSocket.instances).toHaveLength(2);
      const replacement = MockWebSocket.instances[1];
      replacement?.emitOpen();
      await flushPromises();
      const outbound = (replacement?.sent ?? []).map((raw) => JSON.parse(raw) as RelayEvent);
      const send = outbound.find((event) => event.type === RelayEventType.MessageSend);
      const ask = outbound.find((event) => event.type === RelayEventType.MessageAsk);
      expect(send?.id).toBeDefined();
      expect(ask?.id).toBeDefined();
      replacement?.emitMessage({ type: "message.ack", replyTo: send?.id, payload: { status: "delivered", deviceId: "dev_2" } });
      replacement?.emitMessage({ type: "message.ack", replyTo: ask?.id, payload: { status: "delivered", deviceId: "dev_2" } });
      await Promise.all([sendPromise, askPromise]);
      expect(MockWebSocket.instances).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not let an old heartbeat timeout close a replacement socket", async () => {
    vi.useFakeTimers();
    try {
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
      );
      await connectAndOpen(client);
      const original = MockWebSocket.instances[0];
      await vi.advanceTimersByTimeAsync(25_000);
      original?.close(1006, "network gone during heartbeat");
      await vi.advanceTimersByTimeAsync(250);
      await flushPromises();

      const replacement = MockWebSocket.instances[1];
      expect(replacement).toBeDefined();
      replacement?.emitOpen();
      await flushPromises();
      await vi.advanceTimersByTimeAsync(4_750);
      await flushPromises();

      expect(replacement?.readyState).toBe(1);
      expect(MockWebSocket.instances).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a manual connect fences the old socket and heartbeat before bootstrap", async () => {
    vi.useFakeTimers();
    try {
      const manualBootstrap = deferred<FetchResponseLike>();
      let fetchCount = 0;
      const fetchImpl = vi.fn<FetchLike>(() => {
        fetchCount += 1;
        if (fetchCount === 1) {
          return Promise.resolve({ ok: true, status: 200, json: async () => connectPayload() });
        }
        return manualBootstrap.promise;
      });
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: fetchImpl, WebSocket: MockWebSocket },
      );
      await connectAndOpen(client);
      const original = MockWebSocket.instances[0];
      await vi.advanceTimersByTimeAsync(25_000);

      const manualConnect = client.connect("new-channel", "9999");
      expect(original?.readyState).toBe(3);
      manualBootstrap.resolve({
        ok: true,
        status: 200,
        json: async () => connectPayload({ channelId: "ch_new", token: "new_token" }),
      });
      await flushPromises();
      const replacement = MockWebSocket.instances[1];
      replacement?.emitOpen();
      await manualConnect;
      await vi.advanceTimersByTimeAsync(5_000);
      await flushPromises();

      expect(replacement?.readyState).toBe(1);
      expect(MockWebSocket.instances).toHaveLength(2);
      expect(client.channelId).toBe("ch_new");
    } finally {
      vi.useRealTimers();
    }
  });

  it("a manual connect cancels an older reconnect loop", async () => {
    vi.useFakeTimers();
    try {
      const manualBootstrap = deferred<FetchResponseLike>();
      let fetchCount = 0;
      const fetchImpl = vi.fn<FetchLike>(() => {
        fetchCount += 1;
        if (fetchCount === 1) {
          return Promise.resolve({ ok: true, status: 200, json: async () => connectPayload() });
        }
        if (fetchCount === 2) {
          return manualBootstrap.promise;
        }
        return Promise.resolve({ ok: true, status: 200, json: async () => connectPayload({ channelId: "ch_old" }) });
      });
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: fetchImpl, WebSocket: MockWebSocket },
      );
      await connectAndOpen(client);
      MockWebSocket.instances[0]?.close(1006, "network gone");

      const manualConnect = client.connect("new-channel", "9999");
      await flushPromises();
      expect(fetchImpl).toHaveBeenCalledTimes(2);
      manualBootstrap.resolve({
        ok: true,
        status: 200,
        json: async () => connectPayload({ channelId: "ch_new", token: "new_token" }),
      });
      await flushPromises();
      expect(MockWebSocket.instances).toHaveLength(2);
      MockWebSocket.instances[1]?.emitOpen();
      await expect(manualConnect).resolves.toEqual(expect.objectContaining({ channelId: "ch_new" }));
      expect(client.channelId).toBe("ch_new");
    } finally {
      vi.useRealTimers();
    }
  });

  it("explicit disconnect cancels an in-flight reconnect bootstrap", async () => {
    vi.useFakeTimers();
    try {
      const reconnectBootstrap = deferred<FetchResponseLike>();
      let fetchCount = 0;
      const fetchImpl = vi.fn<FetchLike>(() => {
        fetchCount += 1;
        if (fetchCount === 1) {
          return Promise.resolve({ ok: true, status: 200, json: async () => connectPayload() });
        }
        return reconnectBootstrap.promise;
      });
      const client = new RelayClient(
        { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
        { fetch: fetchImpl, WebSocket: MockWebSocket, reconnectDelayMs: 5 },
      );
      await connectAndOpen(client);
      MockWebSocket.instances[0]?.close(1006, "network gone");
      await vi.advanceTimersByTimeAsync(5);
      await flushPromises();
      expect(fetchImpl).toHaveBeenCalledTimes(2);

      client.disconnect();
      reconnectBootstrap.resolve({ ok: true, status: 200, json: async () => connectPayload({ token: "late_token" }) });
      await flushPromises();

      expect(MockWebSocket.instances).toHaveLength(1);
      expect(client.connectState).toEqual(expect.objectContaining({ transportStatus: "disconnected" }));
    } finally {
      vi.useRealTimers();
    }
  });

  it("reconnects an established member socket after close", async () => {
    vi.useFakeTimers();
    let connectCount = 0;
    const fetchImpl = vi.fn<FetchLike>(async () => {
      connectCount += 1;
      return {
        ok: true,
        status: 200,
        json: async () => connectPayload({ token: `token_${connectCount}` }),
      };
    });
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: fetchImpl, WebSocket: MockWebSocket, reconnectDelayMs: 5 },
    );

    try {
      await connectAndOpen(client);
      MockWebSocket.instances[0]?.close();
      await vi.advanceTimersByTimeAsync(5);
      await flushPromises();

      expect(fetchImpl).toHaveBeenCalledTimes(2);
      expect(MockWebSocket.instances).toHaveLength(2);
      expect(MockWebSocket.instances[1]?.url).toBe("ws://relay.example/ws?token=token_2");
      expect(JSON.parse(String(fetchImpl.mock.calls[1]?.[1]?.body))).toEqual(expect.objectContaining({ deviceId: "dev_1" }));
    } finally {
      vi.useRealTimers();
    }
  });

  it("throws useful errors for non-2xx JSON responses", async () => {
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch({ error: "bad pin" }, { ok: false, status: 403 }), WebSocket: MockWebSocket },
    );

    await expect(client.connect("dwkim", "wrong")).rejects.toMatchObject({
      message: "bad pin",
      status: 403,
      details: { error: "bad pin" },
    });
    expect(MockWebSocket.instances).toHaveLength(0);
  });
});
