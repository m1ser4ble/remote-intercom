import { describe, expect, it, vi } from "vitest";

import { RelayClient, type FetchLike, type WebSocketLike } from "./relay-client.js";
import { RelayEventType, type ConnectResponse, type RelayEvent } from "./protocol.js";

class MockWebSocket implements WebSocketLike {
  static instances: MockWebSocket[] = [];

  readonly url: string;
  readyState = 1;
  sent: string[] = [];
  private handlers = new Map<string, Set<(...args: unknown[]) => void>>();

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.readyState = 3;
    this.emit("close");
  }

  on(event: "message" | "close" | "error", handler: (...args: unknown[]) => void): void {
    const handlers = this.handlers.get(event) ?? new Set<(...args: unknown[]) => void>();
    handlers.add(handler);
    this.handlers.set(event, handlers);
  }

  emitMessage(event: RelayEvent): void {
    this.emit("message", JSON.stringify(event));
  }

  private emit(event: string, ...args: unknown[]): void {
    for (const handler of this.handlers.get(event) ?? []) {
      handler(...args);
    }
  }
}

function resetSockets(): void {
  MockWebSocket.instances = [];
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

function mockFetch(payload: unknown, init: { ok?: boolean; status?: number; statusText?: string } = {}): FetchLike {
  return vi.fn(async () => ({
    ok: init.ok ?? true,
    status: init.status ?? 200,
    statusText: init.statusText,
    json: async () => payload,
  }));
}

describe("RelayClient", () => {
  it("posts connect body and opens WebSocket with token query", async () => {
    resetSockets();
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

    const response = await client.connect("dwkim", "1234");

    expect(response.token).toBe("token with spaces");
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
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(MockWebSocket.instances[0]?.url).toBe("ws://relay.example/ws?token=token+with+spaces");
  });

  it("invokes registered handlers for incoming WebSocket messages", async () => {
    resetSockets();
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      { fetch: mockFetch(connectPayload()), WebSocket: MockWebSocket },
    );
    const handler = vi.fn();
    client.on(RelayEventType.MessageSend, handler);

    await client.connect("dwkim", "1234");
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

  it("serializes send, ask, reply, join decisions, list, and status events", async () => {
    resetSockets();
    let counter = 0;
    const client = new RelayClient(
      { relayHttpUrl: "http://relay.example", deviceName: "test-device" },
      {
        fetch: mockFetch(connectPayload()),
        WebSocket: MockWebSocket,
        idGenerator: () => `evt_${++counter}`,
      },
    );
    await client.connect("dwkim", "1234");
    const socket = MockWebSocket.instances[0];
    expect(socket).toBeDefined();

    client.send("dev_2", "hello");
    client.ask("dev_2", "question?");
    socket?.emitMessage({ id: "ask_in", type: RelayEventType.MessageSend, from: "dev_2", to: "dev_1", payload: { text: "remote question?", kind: "ask" } });
    client.reply("ask_in", "answer");
    client.approveJoin("join_1");
    client.denyJoin("join_2");
    client.list();
    client.status();

    const events = socket?.sent.map((raw) => JSON.parse(raw) as RelayEvent) ?? [];
    expect(events).toEqual([
      expect.objectContaining({ id: "evt_1", type: RelayEventType.MessageSend, channelId: "ch_1", to: "dev_2", payload: { text: "hello", kind: "send" } }),
      expect.objectContaining({ id: "evt_2", type: RelayEventType.MessageSend, channelId: "ch_1", to: "dev_2", payload: { text: "question?", kind: "ask" } }),
      expect.objectContaining({ id: "evt_3", type: RelayEventType.MessageSend, channelId: "ch_1", to: "dev_2", replyTo: "ask_in", payload: { text: "answer", kind: "reply" } }),
      expect.objectContaining({ id: "evt_4", type: RelayEventType.JoinApprove, channelId: "ch_1", payload: { joinRequestId: "join_1" } }),
      expect.objectContaining({ id: "evt_5", type: RelayEventType.JoinDeny, channelId: "ch_1", payload: { joinRequestId: "join_2" } }),
      expect.objectContaining({ id: "evt_6", type: RelayEventType.ListRequest, channelId: "ch_1" }),
      expect.objectContaining({ id: "evt_7", type: RelayEventType.StatusRequest, channelId: "ch_1" }),
    ]);
  });

  it("throws useful errors for non-2xx JSON responses", async () => {
    resetSockets();
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
