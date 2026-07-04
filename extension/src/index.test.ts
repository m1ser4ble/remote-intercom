import { describe, expect, test, vi } from "vitest";

import remoteIntercomExtension, { createRemoteIntercomExtension } from "./index.js";

describe("pi extension adapter", () => {
  test("default export registers a pi-compatible remote_intercom tool", async () => {
    let registered: any;
    const api = {
      registerTool: vi.fn((tool: any) => {
        registered = tool;
      }),
      sendMessage: vi.fn(),
    };

    remoteIntercomExtension(api as any);

    expect(api.registerTool).toHaveBeenCalledTimes(1);
    expect(registered.name).toBe("remote_intercom");
    expect(registered.parameters).toBeDefined();
    expect(typeof registered.execute).toBe("function");

    const result = await registered.execute("tool-call-1", { action: "pending" }, new AbortController().signal);

    expect(result.content[0].type).toBe("text");
    expect(result.details.ok).toBe(true);
    expect(result.details.action).toBe("pending");
  });

  test("factory activation registers pi-compatible tool instead of internal handler", () => {
    let registered: any;
    const extension = createRemoteIntercomExtension();

    extension.activate({
      registerTool(tool: any) {
        registered = tool;
      },
    });

    expect(registered.name).toBe("remote_intercom");
    expect(registered).not.toBe(extension.tool);
    expect(typeof registered.execute).toBe("function");
  });
});
