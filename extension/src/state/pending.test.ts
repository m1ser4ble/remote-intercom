import { describe, expect, it } from "vitest";

import { PendingState } from "./pending.js";

describe("PendingState", () => {
  it("tracks pending asks and join requests by id", () => {
    const pending = new PendingState();

    pending.addAsk({
      id: "ask_1",
      from: "dev_1",
      message: "question?",
      receivedAt: "2026-07-04T00:00:00.000Z",
    });
    pending.addJoinRequest({
      id: "join_1",
      joinRequestId: "join_1",
      deviceId: "dev_2",
      deviceName: "Laptop",
      channelName: "dwkim",
      receivedAt: "2026-07-04T00:00:01.000Z",
    });

    expect(pending.getAsk("ask_1")?.message).toBe("question?");
    expect(pending.getJoinRequest("join_1")?.deviceName).toBe("Laptop");
    expect(pending.snapshot()).toEqual({
      asks: [expect.objectContaining({ id: "ask_1" })],
      joinRequests: [expect.objectContaining({ id: "join_1" })],
    });

    expect(pending.deleteAsk("ask_1")).toBe(true);
    expect(pending.deleteJoinRequest("join_1")).toBe(true);
    expect(pending.snapshot()).toEqual({ asks: [], joinRequests: [] });
  });
});
