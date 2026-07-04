#!/usr/bin/env sh
set -eu

RELAY_HTTP_URL={{ .RelayHTTPURLShell }}
RELAY_WS_URL={{ .RelayWSURLShell }}
export RELAY_HTTP_URL RELAY_WS_URL

CONFIG_DIR="${PI_REMOTE_INTERCOM_CONFIG_DIR:-${HOME}/.pi/remote-intercom}"
CONFIG_FILE="${CONFIG_DIR}/config.json"

mkdir -p "${CONFIG_DIR}"
umask 077
TMP_FILE="${CONFIG_FILE}.tmp.$$"

cat > "${TMP_FILE}" <<'EOF_CONFIG'
{
  "relayHttpUrl": {{ .RelayHTTPURLJSON }},
  "relayWsUrl": {{ .RelayWSURLJSON }}
}
EOF_CONFIG
mv "${TMP_FILE}" "${CONFIG_FILE}"

printf '%s\n' "Remote Intercom relay config written to ${CONFIG_FILE}"
printf '%s\n' "  HTTP: ${RELAY_HTTP_URL}"
printf '%s\n' "  WS:   ${RELAY_WS_URL}"

if command -v pi >/dev/null 2>&1; then
  PI_CMD=$(command -v pi)
  printf '%s\n' "Detected pi command at ${PI_CMD}."
else
  printf '%s\n' "pi command was not found on PATH. Install pi before loading the extension."
fi

cat <<'EOF_NEXT'

Next steps (MVP/local package):
  1. From a remote-intercom checkout, build the extension:
       cd extension && npm ci && npm run build
  2. Load/register the built extension with pi according to your pi extension workflow.
  3. Pass the relay settings from ~/.pi/remote-intercom/config.json to the extension when registering it.

The extension package is not published by this MVP installer, so this script only writes safe local configuration.
EOF_NEXT
