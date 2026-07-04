# Security model

Remote Intercom is an MVP relay for trusted operators. It should be self-hosted behind HTTPS and treated as sensitive infrastructure.

## Channel access

A channel is keyed by `channelName` plus `PIN`. The relay normalizes the channel name and combines it with the PIN to derive an in-memory channel id. Anyone who knows both values can request to join.

The first device to create a channel becomes the owner. Later devices enter `pending_approval` and cannot send member messages until the current owner approves them.

## Owner approval

Join approval is enforced by the relay:

- pending devices receive pending JWTs only;
- pending devices can wait on WebSocket for `join.approved` or `join.denied`;
- only the current owner may send `join.approve` or `join.deny` successfully.

Owner status can fail over to another online member if the owner disconnects, then return when the original owner reconnects.

## Tokens

The relay issues HMAC JWTs using `RELAY_TOKEN_SECRET`:

- member tokens allow WebSocket member traffic for one channel/device;
- pending tokens allow only the pending join flow;
- tokens expire according to the relay token manager configuration.

Use a long random `RELAY_TOKEN_SECRET`, keep it private, and avoid logging tokens. If the secret changes, existing tokens stop validating.

## Relay visibility

The MVP does **not** provide end-to-end encryption. The relay can see:

- channel ids and device ids;
- device names;
- join requests and approvals;
- message payloads, asks, and replies.

Run the relay only where this visibility is acceptable. Use HTTPS/WSS so network observers cannot read traffic in transit.

## Installer safety

Prefer inspecting the installer before running it:

```bash
curl -fsSLo remote-intercom-install.sh https://relay.example.com/install.sh
less remote-intercom-install.sh
sh remote-intercom-install.sh
```

The installer uses `set -eu`, quotes substituted relay URLs, downloads the bundled extension from the same relay, and writes only:

- `~/.pi/remote-intercom/config.json` unless `PI_REMOTE_INTERCOM_CONFIG_DIR` is set;
- `~/.pi/agent/extensions/remote-intercom/index.js` unless `PI_REMOTE_INTERCOM_EXTENSION_DIR` is set.

The relay serves the extension bundle from `RELAY_EXTENSION_BUNDLE`; operators should build and review that bundle before exposing `/install.sh`.

## Rate limits and abuse controls

The current relay has basic request-size hardening on JSON endpoints but does not yet include production-grade rate limiting, lockouts, audit logs, or admin controls. Recommended deployment controls:

- put the relay behind a reverse proxy with request rate limits;
- require HTTPS;
- use high-entropy PINs for shared channels;
- rotate `RELAY_TOKEN_SECRET` if tokens may have leaked.

Future work: per-IP and per-channel rate limits, brute-force protection for channel/PIN attempts, audit events, optional end-to-end encryption, and packaged release signing.
