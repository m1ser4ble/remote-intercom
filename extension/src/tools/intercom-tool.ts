import {
  type ConnectResponse,
  type MessagePayload,
  type RelayEvent,
  RelayEventType,
} from "../client/protocol.js";
import { RelayClient, type ConnectOptions, type RelayClientState } from "../client/relay-client.js";
import { PendingState, type PendingSnapshot } from "../state/pending.js";

export type NotifyCallback = (message: string, event?: RelayEvent) => void | Promise<void>;
export type NotifyErrorCallback = (error: unknown, message: string, event?: RelayEvent) => void | Promise<void>;

export interface RemoteIntercomClient {
  connect(channel: string, pin: string, options?: ConnectOptions): Promise<ConnectResponse>;
  list(): RelayEvent;
  send(to: string, message: string): RelayEvent<MessagePayload>;
  ask(to: string, message: string): RelayEvent<MessagePayload>;
  reply(replyTo: string, message: string): RelayEvent<MessagePayload>;
  status(): RelayEvent;
  disconnect(code?: number, reason?: string): void;
  approveJoin(joinRequestId: string): RelayEvent;
  denyJoin(joinRequestId: string): RelayEvent;
  on(eventType: string, handler: (event: RelayEvent) => void): () => void;
  readonly connectState?: RelayClientState;
  readonly currentStatus?: string;
  readonly channelId?: string;
  readonly deviceId?: string;
  readonly webSocket?: { readyState: number };
}

export interface ToolContext {
  client: RemoteIntercomClient;
  pending: PendingState;
  onNotify?: NotifyCallback;
  onNotifyError?: NotifyErrorCallback;
  channelName?: string;
}

export type RemoteIntercomToolInput =
  | {
      action: "connect";
      channel?: string;
      channelName?: string;
      pin: string;
      deviceName?: string;
      deviceId?: string;
      clientVersion?: string;
      signal?: unknown;
    }
  | { action: "list" }
  | { action: "send"; to: string; message: string }
  | { action: "ask"; to: string; message: string }
  | { action: "reply"; id?: string; replyTo?: string; message: string }
  | { action: "pending" }
  | { action: "status" }
  | { action: "disconnect"; code?: number; reason?: string }
  | { action: "approve_join"; id?: string; joinRequestId?: string }
  | { action: "deny_join"; id?: string; joinRequestId?: string };

export type RemoteIntercomToolResult =
  | { ok: true; action: "connect"; response: ConnectResponse; connection: RelayClientState }
  | { ok: true; action: "list" | "send" | "ask" | "reply" | "status" | "approve_join" | "deny_join"; event: RelayEvent; connection: RelayClientState }
  | { ok: true; action: "pending"; pending: PendingSnapshot }
  | { ok: true; action: "disconnect"; connection: RelayClientState };

export interface RemoteIntercomTool {
  name: string;
  description: string;
  execute(input: unknown): Promise<RemoteIntercomToolResult>;
  handle(input: unknown): Promise<RemoteIntercomToolResult>;
  dispose(): void;
}

export interface CreateRemoteIntercomToolOptions {
  client?: RemoteIntercomClient;
  pending?: PendingState;
  onNotify?: NotifyCallback;
  onNotifyError?: NotifyErrorCallback;
}

type ToolRegistrar =
  | ((tool: RemoteIntercomTool) => unknown)
  | {
      registerTool?: (tool: RemoteIntercomTool) => unknown;
      register?: (tool: RemoteIntercomTool) => unknown;
      tool?: (tool: RemoteIntercomTool) => unknown;
      addTool?: (tool: RemoteIntercomTool) => unknown;
    };

export function createRemoteIntercomTool(options: CreateRemoteIntercomToolOptions = {}): RemoteIntercomTool {
  const context: ToolContext = {
    client: options.client ?? new RelayClient(),
    pending: options.pending ?? new PendingState(),
    onNotify: options.onNotify,
    onNotifyError: options.onNotifyError,
  };
  const disposeHandlers = installRelayEventHandlers(context);

  const execute = async (rawInput: unknown): Promise<RemoteIntercomToolResult> => {
    const input = parseToolInput(rawInput);

    switch (input.action) {
      case "connect": {
        const channel = nonEmpty(input.channel) ?? nonEmpty(input.channelName);
        if (channel === undefined) {
          throw new Error("connect requires channel or channelName");
        }
        const previousChannelName = context.channelName;
        const response = await context.client.connect(channel, input.pin, {
          deviceName: input.deviceName,
          deviceId: input.deviceId,
          clientVersion: input.clientVersion,
          signal: input.signal,
        });
        if (previousChannelName !== undefined && previousChannelName !== channel) {
          context.pending.clear();
        }
        context.channelName = channel;
        return { ok: true, action: "connect", response, connection: connectionState(context.client) };
      }
      case "list":
        return { ok: true, action: "list", event: context.client.list(), connection: connectionState(context.client) };
      case "send":
        return { ok: true, action: "send", event: context.client.send(input.to, input.message), connection: connectionState(context.client) };
      case "ask":
        return { ok: true, action: "ask", event: context.client.ask(input.to, input.message), connection: connectionState(context.client) };
      case "reply": {
        const replyTo = nonEmpty(input.replyTo) ?? nonEmpty(input.id);
        if (replyTo === undefined) {
          throw new Error("reply requires replyTo or id");
        }
        const event = context.client.reply(replyTo, input.message);
        context.pending.deleteAsk(replyTo);
        return { ok: true, action: "reply", event, connection: connectionState(context.client) };
      }
      case "pending":
        return { ok: true, action: "pending", pending: context.pending.snapshot() };
      case "status":
        return { ok: true, action: "status", event: context.client.status(), connection: connectionState(context.client) };
      case "disconnect":
        context.client.disconnect(input.code, input.reason);
        context.pending.clear();
        return { ok: true, action: "disconnect", connection: connectionState(context.client) };
      case "approve_join": {
        const joinRequestId = requireJoinRequestId(input);
        const event = context.client.approveJoin(joinRequestId);
        context.pending.deleteJoinRequest(joinRequestId);
        return { ok: true, action: "approve_join", event, connection: connectionState(context.client) };
      }
      case "deny_join": {
        const joinRequestId = requireJoinRequestId(input);
        const event = context.client.denyJoin(joinRequestId);
        context.pending.deleteJoinRequest(joinRequestId);
        return { ok: true, action: "deny_join", event, connection: connectionState(context.client) };
      }
      default:
        return assertNever(input);
    }
  };

  return {
    name: "remote_intercom",
    description: "Connect to a remote intercom relay and exchange list/send/ask/reply/pending/status messages.",
    execute,
    handle: execute,
    dispose: disposeHandlers,
  };
}

export function registerRemoteIntercom(registrar: ToolRegistrar, options: CreateRemoteIntercomToolOptions = {}): RemoteIntercomTool {
  const tool = createRemoteIntercomTool(options);
  if (typeof registrar === "function") {
    registrar(tool);
    return tool;
  }
  const register = registrar.registerTool ?? registrar.register ?? registrar.tool ?? registrar.addTool;
  if (register === undefined) {
    throw new Error("registrar must be a function or expose registerTool/register/tool/addTool");
  }
  register.call(registrar, tool);
  return tool;
}

function installRelayEventHandlers(context: ToolContext): () => void {
  const disposeMessageHandler = context.client.on(RelayEventType.MessageSend, (event) => {
    const payload = event.payload as MessagePayload | undefined;
    if (payload?.kind !== "ask" || event.id === undefined || typeof payload.text !== "string") {
      return;
    }
    context.pending.addAsk({
      id: event.id,
      from: event.from,
      to: event.to,
      channelId: event.channelId,
      message: payload.text,
      receivedAt: new Date().toISOString(),
    });
    notify(context, `Device ${event.from ?? "unknown"} asks: ${payload.text}`, event);
  });

  const disposeJoinHandler = context.client.on(RelayEventType.JoinRequest, (event) => {
    const payload = event.payload;
    const joinRequestId = validNonEmptyString(payload?.joinRequestId);
    const deviceId = validNonEmptyString(payload?.deviceId);
    const deviceName = validNonEmptyString(payload?.deviceName);
    const channelId = validNonEmptyString(event.channelId);
    if (joinRequestId === undefined || deviceId === undefined || deviceName === undefined || channelId === undefined) {
      return;
    }
    const channelName = context.channelName ?? channelId;
    context.pending.addJoinRequest({
      id: joinRequestId,
      joinRequestId,
      deviceId,
      deviceName,
      channelId,
      channelName,
      receivedAt: new Date().toISOString(),
    });
    notify(
      context,
      `Device ${deviceName} wants to join ${channelName} (joinRequestId: ${joinRequestId}). Use approve_join or deny_join with joinRequestId "${joinRequestId}".`,
      event,
    );
  });

  return () => {
    disposeMessageHandler();
    disposeJoinHandler();
  };
}

const TOOL_ACTIONS = [
  "connect",
  "list",
  "send",
  "ask",
  "reply",
  "pending",
  "status",
  "disconnect",
  "approve_join",
  "deny_join",
] as const;
const TOOL_ACTION_SET = new Set<string>(TOOL_ACTIONS);

function parseToolInput(input: unknown): RemoteIntercomToolInput {
  if (!isRecord(input)) {
    throw new Error("remote_intercom input must be a non-null object");
  }

  const action = input.action;
  if (typeof action !== "string" || !TOOL_ACTION_SET.has(action)) {
    throw new Error(`remote_intercom action must be one of: ${TOOL_ACTIONS.join(", ")}`);
  }

  switch (action) {
    case "connect":
      optionalNonEmptyString(input, "channel", "connect");
      optionalNonEmptyString(input, "channelName", "connect");
      optionalNonEmptyString(input, "deviceName", "connect");
      optionalNonEmptyString(input, "deviceId", "connect");
      optionalNonEmptyString(input, "clientVersion", "connect");
      requireAtLeastOneNonEmptyString(input, ["channel", "channelName"], "connect");
      requiredNonEmptyString(input, "pin", "connect");
      return input as RemoteIntercomToolInput;
    case "send":
    case "ask":
      requiredNonEmptyString(input, "to", action);
      requiredNonEmptyString(input, "message", action);
      return input as RemoteIntercomToolInput;
    case "reply":
      optionalNonEmptyString(input, "replyTo", "reply");
      optionalNonEmptyString(input, "id", "reply");
      requireAtLeastOneNonEmptyString(input, ["replyTo", "id"], "reply");
      requiredNonEmptyString(input, "message", "reply");
      return input as RemoteIntercomToolInput;
    case "disconnect":
      if (input.code !== undefined && (typeof input.code !== "number" || !Number.isFinite(input.code))) {
        throw new Error("disconnect field code must be a finite number when provided");
      }
      if (input.reason !== undefined && typeof input.reason !== "string") {
        throw new Error("disconnect field reason must be a string when provided");
      }
      return input as RemoteIntercomToolInput;
    case "approve_join":
    case "deny_join":
      optionalNonEmptyString(input, "joinRequestId", action);
      optionalNonEmptyString(input, "id", action);
      requireAtLeastOneNonEmptyString(input, ["joinRequestId", "id"], action);
      return input as RemoteIntercomToolInput;
    case "list":
    case "pending":
    case "status":
      return input as RemoteIntercomToolInput;
    default:
      throw new Error(`remote_intercom action must be one of: ${TOOL_ACTIONS.join(", ")}`);
  }
}

function requireJoinRequestId(input: { id?: string; joinRequestId?: string }): string {
  const joinRequestId = nonEmpty(input.joinRequestId) ?? nonEmpty(input.id);
  if (joinRequestId === undefined) {
    throw new Error("join approval requires joinRequestId or id");
  }
  return joinRequestId;
}

function connectionState(client: RemoteIntercomClient): RelayClientState {
  return {
    ...(client.connectState ?? {}),
    ...(client.currentStatus === undefined ? {} : { status: client.currentStatus }),
    ...(client.channelId === undefined ? {} : { channelId: client.channelId }),
    ...(client.deviceId === undefined ? {} : { deviceId: client.deviceId }),
  };
}

function notify(context: ToolContext, message: string, event: RelayEvent): void {
  if (context.onNotify === undefined) {
    return;
  }
  try {
    void Promise.resolve(context.onNotify(message, event)).catch((error: unknown) => {
      reportNotifyError(context, error, message, event);
    });
  } catch (error) {
    reportNotifyError(context, error, message, event);
  }
}

function reportNotifyError(context: ToolContext, error: unknown, message: string, event: RelayEvent): void {
  if (context.onNotifyError === undefined) {
    return;
  }
  try {
    void Promise.resolve(context.onNotifyError(error, message, event)).catch(() => undefined);
  } catch {
    // Avoid surfacing notification-error handler failures through relay event handlers.
  }
}

function requiredNonEmptyString(input: Record<string, unknown>, field: string, action: string): string {
  const value = input[field];
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${action} requires non-empty string field ${field}`);
  }
  return value;
}

function optionalNonEmptyString(input: Record<string, unknown>, field: string, action: string): string | undefined {
  const value = input[field];
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${action} field ${field} must be a non-empty string when provided`);
  }
  return value;
}

function requireAtLeastOneNonEmptyString(input: Record<string, unknown>, fields: string[], action: string): string {
  for (const field of fields) {
    const value = input[field];
    if (typeof value === "string" && value.trim() !== "") {
      return value;
    }
  }
  throw new Error(`${action} requires one of: ${fields.join(", ")}`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validNonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

function nonEmpty(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed === "" ? undefined : trimmed;
}

function assertNever(value: never): never {
  throw new Error(`unsupported remote intercom action ${(value as { action?: string }).action ?? "unknown"}`);
}
