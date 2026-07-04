import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export const DEFAULT_RELAY_HTTP_URL = "http://127.0.0.1:8080";
export const DEFAULT_RELAY_WS_URL = "ws://127.0.0.1:8080/ws";
export const DEFAULT_DEVICE_NAME = "remote-intercom-extension";

export interface RelayClientConfig {
  relayHttpUrl?: string;
  relayWsUrl?: string;
  deviceName?: string;
  deviceId?: string;
  clientVersion?: string;
}

export interface NormalizedRelayClientConfig {
  relayHttpUrl: string;
  relayWsUrl: string;
  deviceName: string;
  deviceId?: string;
  clientVersion?: string;
}

export function normalizeBaseUrl(rawUrl: string): string {
  const trimmed = rawUrl.trim();
  if (trimmed === "") {
    throw new Error("relay URL must not be empty");
  }
  return trimmed.replace(/\/+$/, "");
}

export function normalizeConfig(config: RelayClientConfig = {}): NormalizedRelayClientConfig {
  const merged = { ...loadDefaultConfigFile(), ...config };
  return {
    relayHttpUrl: normalizeBaseUrl(merged.relayHttpUrl ?? DEFAULT_RELAY_HTTP_URL),
    relayWsUrl: normalizeBaseUrl(merged.relayWsUrl ?? DEFAULT_RELAY_WS_URL),
    deviceName: nonEmpty(merged.deviceName) ?? defaultDeviceName(),
    deviceId: nonEmpty(merged.deviceId),
    clientVersion: nonEmpty(merged.clientVersion),
  };
}

export function defaultConfigPath(): string {
  const configuredDir = nonEmpty(process.env.PI_REMOTE_INTERCOM_CONFIG_DIR);
  return join(configuredDir ?? join(homedir(), ".pi", "remote-intercom"), "config.json");
}

export function loadDefaultConfigFile(): RelayClientConfig {
  const path = defaultConfigPath();
  if (!existsSync(path)) {
    return {};
  }
  const parsed = JSON.parse(readFileSync(path, "utf8")) as unknown;
  if (!isRecord(parsed)) {
    return {};
  }
  return {
    relayHttpUrl: stringValue(parsed.relayHttpUrl),
    relayWsUrl: stringValue(parsed.relayWsUrl),
    deviceName: stringValue(parsed.deviceName),
    deviceId: stringValue(parsed.deviceId),
    clientVersion: stringValue(parsed.clientVersion),
  };
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function nonEmpty(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed === "" ? undefined : trimmed;
}

function defaultDeviceName(): string {
  const hostname = process.env.HOSTNAME?.trim();
  return hostname === undefined || hostname === "" ? DEFAULT_DEVICE_NAME : hostname;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
