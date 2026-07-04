import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

import { defaultConfigPath, normalizeConfig } from "./config.js";

const originalConfigDir = process.env.PI_REMOTE_INTERCOM_CONFIG_DIR;

afterEach(() => {
  if (originalConfigDir === undefined) {
    delete process.env.PI_REMOTE_INTERCOM_CONFIG_DIR;
  } else {
    process.env.PI_REMOTE_INTERCOM_CONFIG_DIR = originalConfigDir;
  }
});

describe("config loading", () => {
  it("loads installer config from PI_REMOTE_INTERCOM_CONFIG_DIR", () => {
    const configDir = mkdtempSync(join(tmpdir(), "remote-intercom-config-"));
    process.env.PI_REMOTE_INTERCOM_CONFIG_DIR = configDir;
    writeFileSync(join(configDir, "config.json"), JSON.stringify({
      relayHttpUrl: "https://relay.example/",
      relayWsUrl: "wss://relay.example/ws/",
      deviceName: "configured-device",
    }));

    expect(defaultConfigPath()).toBe(join(configDir, "config.json"));
    expect(normalizeConfig()).toEqual(expect.objectContaining({
      relayHttpUrl: "https://relay.example",
      relayWsUrl: "wss://relay.example/ws",
      deviceName: "configured-device",
    }));
  });

  it("lets explicit options override installer config", () => {
    const configDir = mkdtempSync(join(tmpdir(), "remote-intercom-config-"));
    process.env.PI_REMOTE_INTERCOM_CONFIG_DIR = configDir;
    writeFileSync(join(configDir, "config.json"), JSON.stringify({
      relayHttpUrl: "https://file.example",
      relayWsUrl: "wss://file.example/ws",
      deviceName: "file-device",
    }));

    expect(normalizeConfig({ relayHttpUrl: "http://option.example/", deviceName: "option-device" })).toEqual(expect.objectContaining({
      relayHttpUrl: "http://option.example",
      relayWsUrl: "wss://file.example/ws",
      deviceName: "option-device",
    }));
  });
});
