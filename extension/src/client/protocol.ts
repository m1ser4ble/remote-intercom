export type ConnectStatus = "created" | "connected" | "pending_approval";

export interface ConnectRequest {
  channelName: string;
  pin: string;
  deviceName: string;
  deviceId?: string;
  clientVersion?: string;
}

export interface ConnectResponse {
  status: ConnectStatus | string;
  channelId: string;
  deviceId: string;
  joinRequestId?: string;
  token: string;
  wsUrl: string;
}

export const RelayEventType = {
  Error: "error",
  MessageSend: "message.send",
  MessageAsk: "message.ask",
  MessageReply: "message.reply",
  MessageBroadcast: "message.broadcast",
  JoinRequest: "join.request",
  JoinApprove: "join.approve",
  JoinDeny: "join.deny",
  JoinApproved: "join.approved",
  JoinDenied: "join.denied",
  ListRequest: "list.request",
  ListResponse: "list.response",
  StatusRequest: "status.request",
  StatusResponse: "status.response",
} as const;

export type RelayEventType = (typeof RelayEventType)[keyof typeof RelayEventType];

export interface RelayEvent<TPayload extends Record<string, unknown> = Record<string, unknown>> {
  id?: string;
  type: RelayEventType | string;
  channelId?: string;
  from?: string;
  to?: string;
  replyTo?: string;
  payload?: TPayload;
}

export interface MessagePayload extends Record<string, unknown> {
  text: string;
  kind?: "send" | "ask" | "reply";
}

export interface JoinRequestPayload extends Record<string, unknown> {
  joinRequestId: string;
  deviceId: string;
  deviceName: string;
}

export interface JoinDecisionPayload extends Record<string, unknown> {
  joinRequestId: string;
}

export interface ListMember extends Record<string, unknown> {
  deviceId: string;
  deviceName: string;
  online: boolean;
  owner: boolean;
}

export interface ListResponsePayload extends Record<string, unknown> {
  ownerId: string;
  members: ListMember[];
}

export interface StatusResponsePayload extends Record<string, unknown> {
  status: string;
  channelId: string;
  deviceId: string;
  ownerId: string;
  joinRequestId?: string;
}

export interface ErrorPayload extends Record<string, unknown> {
  code: string;
  message: string;
  replyTo?: string;
}
