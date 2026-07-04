import DefaultWebSocket from "ws";

import {
  type ConnectRequest,
  type ConnectResponse,
  type JoinDecisionPayload,
  type MessagePayload,
  type RelayEvent,
  RelayEventType,
} from "./protocol.js";
import { type RelayClientConfig, normalizeConfig } from "../state/config.js";

const WEBSOCKET_OPEN = 1;

type FetchInit = {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  signal?: unknown;
};

export type FetchLike = (input: string, init?: FetchInit) => Promise<FetchResponseLike>;

export interface FetchResponseLike {
  ok: boolean;
  status: number;
  statusText?: string;
  json(): Promise<unknown>;
  text?(): Promise<string>;
}

export interface WebSocketLike {
  readyState: number;
  send(data: string): void;
  close?(code?: number, reason?: string): void;
  addEventListener?(event: "message" | "close" | "error", handler: (event: unknown) => void): void;
  removeEventListener?(event: "message" | "close" | "error", handler: (event: unknown) => void): void;
  on?(event: "message" | "close" | "error", handler: (...args: unknown[]) => void): void;
  off?(event: "message" | "close" | "error", handler: (...args: unknown[]) => void): void;
  removeListener?(event: "message" | "close" | "error", handler: (...args: unknown[]) => void): void;
}

export type WebSocketConstructorLike = new (url: string) => WebSocketLike;
export type RelayEventHandler = (event: RelayEvent) => void;
export type IdGenerator = () => string;

export interface RelayClientDependencies {
  fetch?: FetchLike;
  WebSocket?: WebSocketConstructorLike;
  idGenerator?: IdGenerator;
}

export interface ConnectOptions {
  deviceName?: string;
  deviceId?: string;
  clientVersion?: string;
  signal?: unknown;
}

export interface RelayClientState {
  token?: string;
  channelId?: string;
  deviceId?: string;
  status?: string;
  joinRequestId?: string;
  wsUrl?: string;
}

export class RelayClientError extends Error {
  readonly status?: number;
  readonly details?: unknown;

  constructor(message: string, status?: number, details?: unknown) {
    super(message);
    this.name = "RelayClientError";
    this.status = status;
    this.details = details;
  }
}

export class RelayClient {
  private readonly config;
  private readonly fetchImpl: FetchLike;
  private readonly WebSocketImpl: WebSocketConstructorLike;
  private readonly idGenerator: IdGenerator;
  private readonly handlers = new Map<string, Set<RelayEventHandler>>();
  private readonly replyTargets = new Map<string, string>();
  private socket?: WebSocketLike;
  private state: RelayClientState = {};

  constructor(config: RelayClientConfig = {}, dependencies: RelayClientDependencies = {}) {
    this.config = normalizeConfig(config);
    this.fetchImpl = dependencies.fetch ?? defaultFetch;
    this.WebSocketImpl = dependencies.WebSocket ?? (DefaultWebSocket as unknown as WebSocketConstructorLike);
    this.idGenerator = dependencies.idGenerator ?? defaultIdGenerator;
  }

  get token(): string | undefined {
    return this.state.token;
  }

  get channelId(): string | undefined {
    return this.state.channelId;
  }

  get deviceId(): string | undefined {
    return this.state.deviceId;
  }

  get currentStatus(): string | undefined {
    return this.state.status;
  }

  get connectState(): RelayClientState {
    return { ...this.state };
  }

  get webSocket(): WebSocketLike | undefined {
    return this.socket;
  }

  async connect(channel: string, pin: string, options: ConnectOptions = {}): Promise<ConnectResponse> {
    const request: ConnectRequest = {
      channelName: channel,
      pin,
      deviceName: options.deviceName ?? this.config.deviceName,
    };
    const deviceId = options.deviceId ?? this.config.deviceId;
    const clientVersion = options.clientVersion ?? this.config.clientVersion;
    if (deviceId !== undefined) {
      request.deviceId = deviceId;
    }
    if (clientVersion !== undefined) {
      request.clientVersion = clientVersion;
    }

    const response = await this.fetchImpl(`${this.config.relayHttpUrl}/channels/connect`, {
      method: "POST",
      headers: {
        accept: "application/json",
        "content-type": "application/json",
      },
      body: JSON.stringify(request),
      signal: options.signal,
    });

    const payload = await parseJSONResponse(response);
    if (!response.ok) {
      throw errorFromResponse(response, payload);
    }

    const connectResponse = assertConnectResponse(payload);
    this.state = {
      token: connectResponse.token,
      channelId: connectResponse.channelId,
      deviceId: connectResponse.deviceId,
      status: connectResponse.status,
      joinRequestId: connectResponse.joinRequestId,
      wsUrl: connectResponse.wsUrl,
    };

    this.openSocket(connectResponse.wsUrl || this.config.relayWsUrl, connectResponse.token);
    return connectResponse;
  }

  on(eventType: string, handler: RelayEventHandler): () => void {
    const existing = this.handlers.get(eventType) ?? new Set<RelayEventHandler>();
    existing.add(handler);
    this.handlers.set(eventType, existing);
    return () => this.off(eventType, handler);
  }

  off(eventType: string, handler: RelayEventHandler): void {
    const existing = this.handlers.get(eventType);
    if (existing === undefined) {
      return;
    }
    existing.delete(handler);
    if (existing.size === 0) {
      this.handlers.delete(eventType);
    }
  }

  sendEvent(event: RelayEvent): RelayEvent {
    const socket = this.socket;
    if (socket === undefined || socket.readyState !== WEBSOCKET_OPEN) {
      throw new RelayClientError("relay WebSocket is not connected");
    }

    const outbound: RelayEvent = {
      ...event,
      id: event.id ?? this.idGenerator(),
      channelId: event.channelId ?? this.state.channelId,
    };
    socket.send(JSON.stringify(outbound));
    return outbound;
  }

  send(to: string, message: string): RelayEvent<MessagePayload> {
    return this.sendEvent({
      type: RelayEventType.MessageSend,
      to,
      payload: { text: message, kind: "send" },
    }) as RelayEvent<MessagePayload>;
  }

  ask(to: string, message: string): RelayEvent<MessagePayload> {
    return this.sendEvent({
      type: RelayEventType.MessageSend,
      to,
      payload: { text: message, kind: "ask" },
    }) as RelayEvent<MessagePayload>;
  }

  reply(replyTo: string, message: string): RelayEvent<MessagePayload> {
    const to = this.replyTargets.get(replyTo);
    if (to === undefined) {
      throw new RelayClientError(`no reply target recorded for ${replyTo}`);
    }
    return this.sendEvent({
      type: RelayEventType.MessageSend,
      to,
      replyTo,
      payload: { text: message, kind: "reply" },
    }) as RelayEvent<MessagePayload>;
  }

  approve(joinRequestId: string): RelayEvent<JoinDecisionPayload> {
    return this.sendEvent({
      type: RelayEventType.JoinApprove,
      payload: { joinRequestId },
    }) as RelayEvent<JoinDecisionPayload>;
  }

  deny(joinRequestId: string): RelayEvent<JoinDecisionPayload> {
    return this.sendEvent({
      type: RelayEventType.JoinDeny,
      payload: { joinRequestId },
    }) as RelayEvent<JoinDecisionPayload>;
  }

  approveJoin(joinRequestId: string): RelayEvent<JoinDecisionPayload> {
    return this.approve(joinRequestId);
  }

  denyJoin(joinRequestId: string): RelayEvent<JoinDecisionPayload> {
    return this.deny(joinRequestId);
  }

  list(): RelayEvent {
    return this.sendEvent({ type: RelayEventType.ListRequest });
  }

  status(): RelayEvent {
    return this.sendEvent({ type: RelayEventType.StatusRequest });
  }

  disconnect(code?: number, reason?: string): void {
    const socket = this.socket;
    this.socket = undefined;
    socket?.close?.(code, reason);
  }

  private openSocket(wsUrl: string, token: string): void {
    this.disconnect();
    const socket = new this.WebSocketImpl(socketURLWithToken(wsUrl, token));
    this.socket = socket;

    const messageHandler = (first: unknown): void => {
      const data = messageData(first);
      this.handleSocketMessage(data);
    };
    const closeHandler = (): void => {
      if (this.socket === socket) {
        this.socket = undefined;
      }
    };

    if (socket.addEventListener !== undefined) {
      socket.addEventListener("message", messageHandler);
      socket.addEventListener("close", closeHandler);
    } else if (socket.on !== undefined) {
      socket.on("message", messageHandler);
      socket.on("close", closeHandler);
    }
  }

  private handleSocketMessage(rawData: unknown): void {
    const text = dataToString(rawData);
    if (text === undefined) {
      return;
    }

    let event: RelayEvent;
    try {
      event = JSON.parse(text) as RelayEvent;
    } catch {
      return;
    }

    if (typeof event.type !== "string") {
      return;
    }
    if (event.id !== undefined && event.from !== undefined && event.from !== this.state.deviceId) {
      this.replyTargets.set(event.id, event.from);
    }
    this.emit(event.type, event);
    this.emit("*", event);
  }

  private emit(eventType: string, event: RelayEvent): void {
    const handlers = this.handlers.get(eventType);
    if (handlers === undefined) {
      return;
    }
    for (const handler of [...handlers]) {
      handler(event);
    }
  }
}

async function parseJSONResponse(response: FetchResponseLike): Promise<unknown> {
  try {
    return await response.json();
  } catch (error) {
    if (response.ok) {
      throw new RelayClientError("relay response was not valid JSON", response.status, error);
    }
    return undefined;
  }
}

function errorFromResponse(response: FetchResponseLike, payload: unknown): RelayClientError {
  let message = `relay request failed with HTTP ${response.status}`;
  if (isRecord(payload)) {
    const relayMessage = payload.error ?? payload.message;
    if (typeof relayMessage === "string" && relayMessage.trim() !== "") {
      message = relayMessage;
    }
  } else if (response.statusText !== undefined && response.statusText.trim() !== "") {
    message = response.statusText;
  }
  return new RelayClientError(message, response.status, payload);
}

function assertConnectResponse(payload: unknown): ConnectResponse {
  if (!isRecord(payload)) {
    throw new RelayClientError("relay connect response was not an object");
  }

  const status = payload.status;
  const channelId = payload.channelId;
  const deviceId = payload.deviceId;
  const token = payload.token;
  const wsUrl = payload.wsUrl;
  const joinRequestId = payload.joinRequestId;

  if (
    typeof status !== "string" ||
    typeof channelId !== "string" ||
    typeof deviceId !== "string" ||
    typeof token !== "string" ||
    typeof wsUrl !== "string"
  ) {
    throw new RelayClientError("relay connect response was missing required fields", undefined, payload);
  }
  if (joinRequestId !== undefined && typeof joinRequestId !== "string") {
    throw new RelayClientError("relay connect response had invalid joinRequestId", undefined, payload);
  }

  return {
    status,
    channelId,
    deviceId,
    token,
    wsUrl,
    ...(joinRequestId === undefined ? {} : { joinRequestId }),
  };
}

function socketURLWithToken(wsUrl: string, token: string): string {
  const url = new URL(wsUrl);
  url.searchParams.set("token", token);
  return url.toString();
}

function messageData(messageEventOrData: unknown): unknown {
  if (isRecord(messageEventOrData) && "data" in messageEventOrData) {
    return messageEventOrData.data;
  }
  return messageEventOrData;
}

function dataToString(data: unknown): string | undefined {
  if (typeof data === "string") {
    return data;
  }
  if (data instanceof Uint8Array) {
    return Buffer.from(data).toString("utf8");
  }
  if (data instanceof ArrayBuffer) {
    return Buffer.from(data).toString("utf8");
  }
  if (Array.isArray(data)) {
    return Buffer.concat(data.map((part) => Buffer.isBuffer(part) ? part : Buffer.from(part))).toString("utf8");
  }
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function defaultIdGenerator(): string {
  const cryptoObject = globalThis.crypto;
  if (cryptoObject?.randomUUID !== undefined) {
    return cryptoObject.randomUUID();
  }
  return `evt_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
}

const defaultFetch: FetchLike = async (input, init) => {
  if (globalThis.fetch === undefined) {
    throw new RelayClientError("fetch implementation is not available");
  }
  return globalThis.fetch(input, init as RequestInit) as Promise<FetchResponseLike>;
};
