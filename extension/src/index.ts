import { Type } from "@earendil-works/pi-ai";
import { defineTool, type ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { RelayClient, type RelayClientDependencies } from "./client/relay-client.js";
import { type RelayClientConfig } from "./state/config.js";
import { PendingState } from "./state/pending.js";
import {
  createRemoteIntercomTool,
  registerRemoteIntercom,
  type CreateRemoteIntercomToolOptions,
  type NotifyCallback,
  type RemoteIntercomTool,
  type RemoteIntercomToolResult,
} from "./tools/intercom-tool.js";

export { RelayClient } from "./client/relay-client.js";
export type { RelayClientConfig } from "./state/config.js";
export { PendingState, createPendingState } from "./state/pending.js";
export type { PendingAsk, PendingJoinRequest, PendingSnapshot } from "./state/pending.js";
export {
  createRemoteIntercomTool,
  registerRemoteIntercom,
  type CreateRemoteIntercomToolOptions,
  type NotifyCallback,
  type NotifyErrorCallback,
  type RemoteIntercomTool,
  type RemoteIntercomToolInput,
  type RemoteIntercomToolResult,
  type ToolContext,
} from "./tools/intercom-tool.js";

export interface RemoteIntercomExtensionOptions extends Omit<CreateRemoteIntercomToolOptions, "client" | "pending"> {
  config?: RelayClientConfig;
  dependencies?: RelayClientDependencies;
  client?: CreateRemoteIntercomToolOptions["client"];
  pending?: PendingState;
}

export interface RemoteIntercomExtension {
  name: string;
  tool: RemoteIntercomTool;
  tools: RemoteIntercomTool[];
  activate(api?: unknown): RemoteIntercomTool;
  dispose(): void;
}

export function createRemoteIntercomExtension(options: RemoteIntercomExtensionOptions = {}): RemoteIntercomExtension {
  const client = options.client ?? new RelayClient(options.config, options.dependencies);
  const pending = options.pending ?? new PendingState();
  const tool = createRemoteIntercomTool({
    client,
    pending,
    onNotify: options.onNotify,
    onNotifyError: options.onNotifyError,
  });

  return {
    name: "remote-intercom",
    tool,
    tools: [tool],
    activate(api?: unknown): RemoteIntercomTool {
      if (api !== undefined) {
        registerWithUnknownApi(api, tool);
      }
      return tool;
    },
    dispose(): void {
      tool.dispose();
    },
  };
}

export default function remoteIntercomExtension(api: ExtensionAPI): RemoteIntercomExtension;
export default function remoteIntercomExtension(options?: RemoteIntercomExtensionOptions): RemoteIntercomExtension;
export default function remoteIntercomExtension(input: ExtensionAPI | RemoteIntercomExtensionOptions = {}): RemoteIntercomExtension {
  const options = isExtensionAPI(input)
    ? {
        onNotify: (message: string): void => {
          sendPiNotification(input, message);
        },
      }
    : input;
  const extension = createRemoteIntercomExtension(options);
  if (isExtensionAPI(input)) {
    extension.activate(input);
  }
  return extension;
}

export function toPiTool(tool: RemoteIntercomTool) {
  return defineTool({
    name: tool.name,
    label: "Remote Intercom",
    description: tool.description,
    promptSnippet: "Connect remote pi sessions through a relay channel; supports connect, list, send, ask, reply, pending, status, and join approval.",
    promptGuidelines: [
      "Use remote_intercom when the user asks to connect pi sessions through the remote intercom relay.",
      "Use remote_intercom approve_join or deny_join when a remote join request notification includes a joinRequestId.",
    ],
    parameters: Type.Object({
      action: Type.String({ description: "Action: connect, list, send, ask, reply, pending, status, disconnect, approve_join, or deny_join" }),
      channel: Type.Optional(Type.String()),
      channelName: Type.Optional(Type.String()),
      pin: Type.Optional(Type.String()),
      deviceName: Type.Optional(Type.String()),
      deviceId: Type.Optional(Type.String()),
      clientVersion: Type.Optional(Type.String()),
      to: Type.Optional(Type.String()),
      message: Type.Optional(Type.String()),
      id: Type.Optional(Type.String()),
      replyTo: Type.Optional(Type.String()),
      joinRequestId: Type.Optional(Type.String()),
      code: Type.Optional(Type.Number()),
      reason: Type.Optional(Type.String()),
    }),
    async execute(_toolCallId, params) {
      const result = await tool.execute(params);
      return formatToolResult(result);
    },
  });
}

function registerWithUnknownApi(api: unknown, tool: RemoteIntercomTool): void {
  const piTool = toPiTool(tool);
  if (typeof api === "function") {
    api(piTool);
    return;
  }
  if (typeof api !== "object" || api === null) {
    return;
  }
  const registrar = api as {
    registerTool?: (tool: unknown) => unknown;
    register?: (tool: unknown) => unknown;
    tool?: (tool: unknown) => unknown;
    addTool?: (tool: unknown) => unknown;
  };
  const register = registrar.registerTool ?? registrar.register ?? registrar.tool ?? registrar.addTool;
  register?.call(registrar, piTool);
}

function isExtensionAPI(value: unknown): value is ExtensionAPI {
  return typeof value === "object" && value !== null && typeof (value as { registerTool?: unknown }).registerTool === "function";
}

function sendPiNotification(api: ExtensionAPI, message: string): void {
  const maybeSend = api as ExtensionAPI & {
    sendMessage?: (message: unknown, options?: unknown) => unknown;
  };
  maybeSend.sendMessage?.(
    {
      customType: "remote-intercom",
      content: message,
      display: true,
      details: { source: "remote-intercom" },
    },
    { triggerTurn: true, deliverAs: "steer" },
  );
}

function formatToolResult(result: RemoteIntercomToolResult) {
  return {
    content: [{ type: "text" as const, text: JSON.stringify(result, null, 2) }],
    details: result,
  };
}
