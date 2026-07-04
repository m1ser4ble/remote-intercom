import { describe, expect, it, vi } from "vitest";

import { RelayEventType, type ConnectResponse, type MessagePayload, type RelayEvent } from "../client/protocol.js";
import { PendingState } from "../state/pending.js";
import { createRemoteIntercomTool, type RemoteIntercomClient } from "./intercom-tool.js";

class MockClient implements RemoteIntercomClient {
  connectState = {};
  currentStatus: string | undefined;
  channelId: string | undefined;
  deviceId: string | undefined;
  webSocket: { readyState: number } | undefined;
  readonly sent: RelayEvent[] = [];
  readonly connectCalls: Array<{ channel: string; pin: string; options: unknown }> = [];
  private readonly handlers = new Map<string, Set<(event: RelayEvent) => void>>();

  async connect(channel: string, pin: string, options: unknown): Promise<ConnectResponse> {
    this.connectCalls.push({ channel, pin, options });
    this.connectState = {
      token: "token",
      channelId: "ch_1",
      deviceId: "dev_self",
      status: "created",
      wsUrl: "ws://relay.example/ws",
    };
    this.currentStatus = "created";
    this.channelId = "ch_1";
    this.deviceId = "dev_self";
    this.webSocket = { readyState: 1 };
    return {
      status: "created",
      channelId: "ch_1",
      deviceId: "dev_self",
      token: "token",
      wsUrl: "ws://relay.example/ws",
    };
  }

  list(): RelayEvent {
    return this.record({ id: "evt_list", type: RelayEventType.ListRequest });
  }

  send(to: string, message: string): RelayEvent<MessagePayload> {
    return this.record({ id: "evt_send", type: RelayEventType.MessageSend, to, payload: { text: message, kind: "send" } });
  }

  ask(to: string, message: string): RelayEvent<MessagePayload> {
    return this.record({ id: "evt_ask", type: RelayEventType.MessageSend, to, payload: { text: message, kind: "ask" } });
  }

  reply(replyTo: string, message: string): RelayEvent<MessagePayload> {
    return this.record({ id: "evt_reply", type: RelayEventType.MessageSend, replyTo, payload: { text: message, kind: "reply" } });
  }

  status(): RelayEvent {
    return this.record({ id: "evt_status", type: RelayEventType.StatusRequest });
  }

  disconnect(): void {
    this.webSocket = undefined;
    this.currentStatus = "disconnected";
    this.connectState = { ...this.connectState, status: "disconnected" };
  }

  approveJoin(joinRequestId: string): RelayEvent {
    return this.record({ id: "evt_approve", type: RelayEventType.JoinApprove, payload: { joinRequestId } });
  }

  denyJoin(joinRequestId: string): RelayEvent {
    return this.record({ id: "evt_deny", type: RelayEventType.JoinDeny, payload: { joinRequestId } });
  }

  on(eventType: string, handler: (event: RelayEvent) => void): () => void {
    const handlers = this.handlers.get(eventType) ?? new Set<(event: RelayEvent) => void>();
    handlers.add(handler);
    this.handlers.set(eventType, handlers);
    return () => handlers.delete(handler);
  }

  emit(event: RelayEvent): void {
    for (const handler of [...(this.handlers.get(event.type) ?? [])]) {
      handler(event);
    }
  }

  private record<TPayload extends Record<string, unknown> = Record<string, unknown>>(event: RelayEvent<TPayload>): RelayEvent<TPayload> {
    const outbound = { channelId: this.channelId, ...event };
    this.sent.push(outbound);
    return outbound;
  }
}

describe("remote intercom tool", () => {
  it("connects, sends, requests status, and disconnects through the relay client", async () => {
    const client = new MockClient();
    const tool = createRemoteIntercomTool({ client });

    const connectResult = await tool.execute({ action: "connect", channel: "dwkim", pin: "1234", deviceName: "agent" });
    expect(connectResult).toEqual(expect.objectContaining({
      ok: true,
      action: "connect",
      response: expect.objectContaining({ channelId: "ch_1", deviceId: "dev_self" }),
      connection: expect.objectContaining({ channelId: "ch_1", deviceId: "dev_self", status: "created" }),
    }));
    expect(client.connectCalls).toEqual([
      expect.objectContaining({
        channel: "dwkim",
        pin: "1234",
        options: expect.objectContaining({ deviceName: "agent" }),
      }),
    ]);

    await tool.execute({ action: "send", to: "dev_2", message: "hello" });
    const statusResult = await tool.execute({ action: "status" });
    expect(statusResult).toEqual(expect.objectContaining({
      ok: true,
      action: "status",
      event: expect.objectContaining({ type: RelayEventType.StatusRequest }),
      connection: expect.objectContaining({ channelId: "ch_1", status: "created" }),
    }));
    expect(client.sent).toEqual([
      expect.objectContaining({ type: RelayEventType.MessageSend, to: "dev_2", payload: { text: "hello", kind: "send" } }),
      expect.objectContaining({ type: RelayEventType.StatusRequest }),
    ]);

    await tool.execute({ action: "disconnect" });
    expect(client.currentStatus).toBe("disconnected");
  });

  it("stores join requests, notifies the agent, and approves or denies through tool actions", async () => {
    const client = new MockClient();
    const pending = new PendingState();
    const onNotify = vi.fn();
    const tool = createRemoteIntercomTool({ client, pending, onNotify });

    await tool.execute({ action: "connect", channel: "dwkim", pin: "1234" });
    client.emit({
      id: "evt_join",
      type: RelayEventType.JoinRequest,
      channelId: "ch_1",
      payload: {
        joinRequestId: "join_1",
        deviceId: "dev_2",
        deviceName: "Laptop",
      },
    });

    expect(onNotify).toHaveBeenCalledWith("Device Laptop wants to join dwkim. Approve?", expect.objectContaining({ type: RelayEventType.JoinRequest }));
    const pendingResult = await tool.execute({ action: "pending" });
    expect(pendingResult.action).toBe("pending");
    if (pendingResult.action !== "pending") {
      throw new Error("expected pending result");
    }
    expect(pendingResult.pending.joinRequests).toEqual([
      expect.objectContaining({ id: "join_1", deviceName: "Laptop", channelName: "dwkim" }),
    ]);

    await tool.execute({ action: "approve_join", joinRequestId: "join_1" });
    expect(pending.listJoinRequests()).toEqual([]);
    expect(client.sent).toContainEqual(expect.objectContaining({ type: RelayEventType.JoinApprove, payload: { joinRequestId: "join_1" } }));

    client.emit({
      id: "evt_join_2",
      type: RelayEventType.JoinRequest,
      channelId: "ch_1",
      payload: {
        joinRequestId: "join_2",
        deviceId: "dev_3",
        deviceName: "Tablet",
      },
    });
    await tool.execute({ action: "deny_join", id: "join_2" });
    expect(client.sent).toContainEqual(expect.objectContaining({ type: RelayEventType.JoinDeny, payload: { joinRequestId: "join_2" } }));
  });

  it("stores inbound asks and removes them when replied to", async () => {
    const client = new MockClient();
    const pending = new PendingState();
    const onNotify = vi.fn();
    const tool = createRemoteIntercomTool({ client, pending, onNotify });

    client.emit({
      id: "ask_1",
      type: RelayEventType.MessageSend,
      from: "dev_2",
      to: "dev_self",
      payload: { text: "ready?", kind: "ask" },
    });

    expect(pending.getAsk("ask_1")?.message).toBe("ready?");
    expect(onNotify).toHaveBeenCalledWith("Device dev_2 asks: ready?", expect.objectContaining({ id: "ask_1" }));

    await tool.execute({ action: "reply", replyTo: "ask_1", message: "yes" });
    expect(pending.getAsk("ask_1")).toBeUndefined();
    expect(client.sent).toContainEqual(expect.objectContaining({ type: RelayEventType.MessageSend, replyTo: "ask_1", payload: { text: "yes", kind: "reply" } }));
  });
});
