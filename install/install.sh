#!/usr/bin/env sh
set -eu

RELAY_HTTP_URL={{ .RelayHTTPURLShell }}
RELAY_WS_URL={{ .RelayWSURLShell }}
EXTENSION_URL={{ .ExtensionURLShell }}
export RELAY_HTTP_URL RELAY_WS_URL EXTENSION_URL

CONFIG_DIR="${PI_REMOTE_INTERCOM_CONFIG_DIR:-${HOME}/.pi/remote-intercom}"
CONFIG_FILE="${CONFIG_DIR}/config.json"
EXTENSION_DIR="${PI_REMOTE_INTERCOM_EXTENSION_DIR:-${HOME}/.pi/agent/extensions/remote-intercom}"
EXTENSION_FILE="${EXTENSION_DIR}/index.js"

mkdir -p "${CONFIG_DIR}" "${EXTENSION_DIR}"
umask 077
TMP_FILE="${CONFIG_FILE}.tmp.$$"

cat > "${TMP_FILE}" <<'EOF_CONFIG'
{
  "relayHttpUrl": {{ .RelayHTTPURLJSON }},
  "relayWsUrl": {{ .RelayWSURLJSON }}
}
EOF_CONFIG
mv "${TMP_FILE}" "${CONFIG_FILE}"

EXT_TMP="${EXTENSION_FILE}.tmp.$$"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "${EXTENSION_URL}" -o "${EXT_TMP}"
elif command -v fetch >/dev/null 2>&1; then
  fetch -o "${EXT_TMP}" "${EXTENSION_URL}"
else
  printf '%s\n' "Neither curl nor fetch is available; cannot download Remote Intercom extension." >&2
  exit 1
fi
mv "${EXT_TMP}" "${EXTENSION_FILE}"
chmod 600 "${EXTENSION_FILE}"

printf '%s\n' "Remote Intercom relay config written to ${CONFIG_FILE}"
printf '%s\n' "Remote Intercom pi extension installed to ${EXTENSION_FILE}"
printf '%s\n' "  HTTP: ${RELAY_HTTP_URL}"
printf '%s\n' "  WS:   ${RELAY_WS_URL}"

if command -v pi >/dev/null 2>&1; then
  PI_CMD=$(command -v pi)
  printf '%s\n' "Detected pi command at ${PI_CMD}. Restart pi or run /reload to load remote_intercom."
else
  printf '%s\n' "pi command was not found on PATH. Install pi before loading the extension."
fi
