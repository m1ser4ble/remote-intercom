import { RelayClient, type RelayClientDependencies } from "./client/relay-client.js";
import { type RelayClientConfig } from "./state/config.js";
import { PendingState } from "./state/pending.js";
import {
  createRemoteIntercomTool,
  registerRemoteIntercom,
  type CreateRemoteIntercomToolOptions,
  type NotifyCallback,
  type RemoteIntercomTool,
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

export default function remoteIntercomExtension(options: RemoteIntercomExtensionOptions = {}): RemoteIntercomExtension {
  return createRemoteIntercomExtension(options);
}

function registerWithUnknownApi(api: unknown, tool: RemoteIntercomTool): void {
  if (typeof api === "function") {
    api(tool);
    return;
  }
  if (typeof api !== "object" || api === null) {
    return;
  }
  const registrar = api as {
    registerTool?: (tool: RemoteIntercomTool) => unknown;
    register?: (tool: RemoteIntercomTool) => unknown;
    tool?: (tool: RemoteIntercomTool) => unknown;
    addTool?: (tool: RemoteIntercomTool) => unknown;
  };
  const register = registrar.registerTool ?? registrar.register ?? registrar.tool ?? registrar.addTool;
  register?.call(registrar, tool);
}
