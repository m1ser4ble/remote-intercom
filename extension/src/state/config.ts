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
  return {
    relayHttpUrl: normalizeBaseUrl(config.relayHttpUrl ?? DEFAULT_RELAY_HTTP_URL),
    relayWsUrl: normalizeBaseUrl(config.relayWsUrl ?? DEFAULT_RELAY_WS_URL),
    deviceName: nonEmpty(config.deviceName) ?? defaultDeviceName(),
    deviceId: nonEmpty(config.deviceId),
    clientVersion: nonEmpty(config.clientVersion),
  };
}

function nonEmpty(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed === "" ? undefined : trimmed;
}

function defaultDeviceName(): string {
  const hostname = process.env.HOSTNAME?.trim();
  return hostname === undefined || hostname === "" ? DEFAULT_DEVICE_NAME : hostname;
}
