#!/usr/bin/env bash
#
# install.sh — install rss-proxy as a systemd service.
#
# Idempotent: safe to re-run. It will install/fix the binary, config, and unit
# file, then restart the service only if something changed. If everything is
# already correct and the service is running, it does nothing.
#
# Config is supplied via flags (or env vars) and written to /etc/rss-proxy.env,
# so re-running with new flags updates the live config.
#
# Usage (run with sudo):
#   sudo ./install.sh
#   sudo ./install.sh --public-base-url http://192.168.8.230
#   sudo ./install.sh --public-base-url http://192.168.8.230 --listen :8080 \
#                     --allow-hosts feeds.megaphone.fm,megaphone.imgix.net
#
# Flags (all optional; defaults shown):
#   --public-base-url URL   required in effect; defaults to http://<lan-ip>
#   --listen ADDR           :80
#   --allow-hosts HOSTS     (none = allow any policy-compliant public host)
#   --feed-timeout DUR      15s
#   --max-feed-bytes N      4194304
#   --binary PATH           use this prebuilt binary instead of building from source
#
set -euo pipefail
# set -x # uncomment for debugging

# ---- defaults ----
LISTEN=":80"
ALLOW_HOSTS=""
FEED_TIMEOUT="15s"
MAX_FEED_BYTES="4194304"
PUBLIC_BASE_URL=""
BINARY=""

# ---- parse args ----
while [[ $# -gt 0 ]]; do
	case "$1" in
		--public-base-url) PUBLIC_BASE_URL="$2"; shift 2;;
		--listen)          LISTEN="$2"; shift 2;;
		--allow-hosts)     ALLOW_HOSTS="$2"; shift 2;;
		--feed-timeout)    FEED_TIMEOUT="$2"; shift 2;;
		--max-feed-bytes)  MAX_FEED_BYTES="$2"; shift 2;;
		--binary)          BINARY="$2"; shift 2;;
		-h|--help)
			sed -n '3,28p' "$0"; exit 0;;
		*) echo "unknown flag: $1" >&2; exit 2;;
	esac
done

# ---- root check ----
if [[ $EUID -ne 0 ]]; then
	echo "must run as root (use sudo)" >&2
	exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_BIN="/usr/local/bin/rss-proxy"
ENV_FILE="/etc/rss-proxy.env"
UNIT_FILE="/etc/systemd/system/rss-proxy.service"
SERVICE_USER="rss-proxy"
CHANGED=0

# ---- default public base URL from LAN IP ----
if [[ -z "$PUBLIC_BASE_URL" ]]; then
	LAN_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
	if [[ -z "$LAN_IP" ]]; then
		echo "could not auto-detect LAN IP; pass --public-base-url" >&2
		exit 1
	fi
	PORT="${LISTEN#:}"          # strip leading colon
	if [[ "$PORT" == "80" || -z "$PORT" ]]; then
		PUBLIC_BASE_URL="http://${LAN_IP}"
	else
		PUBLIC_BASE_URL="http://${LAN_IP}:${PORT}"
	fi
	echo "auto-detected public base URL: $PUBLIC_BASE_URL"
fi

# ---- build or locate the binary ----
if [[ -n "$BINARY" ]]; then
	SRC_BIN="$BINARY"
else
	# Prefer a freshly built binary so the service always runs current source.
	if command -v go >/dev/null 2>&1; then
		echo "building rss-proxy from source..."
		(cd "$SCRIPT_DIR" && go build -o "$SCRIPT_DIR/rss-proxy" ./cmd/rss-proxy)
		SRC_BIN="$SCRIPT_DIR/rss-proxy"
	elif [[ -x "$SCRIPT_DIR/rss-proxy" ]]; then
		SRC_BIN="$SCRIPT_DIR/rss-proxy"
		echo "using prebuilt binary $SRC_BIN (go not found; won't rebuild)"
	else
		echo "no binary and no go toolchain; cannot proceed" >&2
		exit 1
	fi
fi

# ---- install the binary (only if it differs) ----
install_binary() {
	if [[ ! -x "$INSTALL_BIN" ]] || ! cmp -s "$SRC_BIN" "$INSTALL_BIN"; then
		echo "installing binary to $INSTALL_BIN"
		install -m 0755 "$SRC_BIN" "$INSTALL_BIN"
		CHANGED=1
	else
		echo "binary already up to date"
	fi
}

# ---- create the service user ----
ensure_user() {
	if ! id "$SERVICE_USER" >/dev/null 2>&1; then
		echo "creating system user $SERVICE_USER"
		useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
	fi
}

# ---- write the env file (only if it differs) ----
write_env() {
	local tmp
	tmp="$(mktemp)"
	{
		echo "# managed by rss-proxy install.sh"
		echo "RSS_PROXY_PUBLIC_BASE_URL=$PUBLIC_BASE_URL"
		echo "RSS_PROXY_LISTEN=$LISTEN"
		[[ -n "$ALLOW_HOSTS" ]] && echo "RSS_PROXY_ALLOW_HOSTS=$ALLOW_HOSTS"
		echo "RSS_PROXY_FEED_TIMEOUT=$FEED_TIMEOUT"
		echo "RSS_PROXY_MAX_FEED_BYTES=$MAX_FEED_BYTES"
	} > "$tmp"
	if [[ ! -f "$ENV_FILE" ]] || ! cmp -s "$tmp" "$ENV_FILE"; then
		echo "writing $ENV_FILE"
		install -m 0640 -o root -g "$SERVICE_USER" "$tmp" "$ENV_FILE"
		CHANGED=1
	else
		echo "$ENV_FILE already up to date"
	fi
	rm -f "$tmp"
}

# ---- write the systemd unit (only if it differs) ----
write_unit() {
	local tmp
	tmp="$(mktemp)"
	cat > "$tmp" <<UNIT
# managed by rss-proxy install.sh — regenerate by re-running it
[Unit]
Description=rss-proxy (podcast HTTPS-to-HTTP compatibility proxy)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_BIN
EnvironmentFile=$ENV_FILE
User=$SERVICE_USER
Group=$SERVICE_USER
# Allow binding low ports (e.g. :80) without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
Restart=on-failure
RestartSec=2
# Light hardening: the proxy only needs network access.
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
UNIT
	if [[ ! -f "$UNIT_FILE" ]] || ! cmp -s "$tmp" "$UNIT_FILE"; then
		echo "writing $UNIT_FILE"
		install -m 0644 "$tmp" "$UNIT_FILE"
		CHANGED=1
	else
		echo "$UNIT_FILE already up to date"
	fi
	rm -f "$tmp"
}

# ---- do the work ----
install_binary
ensure_user
write_env
write_unit

systemctl daemon-reload
systemctl enable "$SERVICE_USER.service" >/dev/null

if [[ "$CHANGED" -eq 1 ]]; then
	echo "config changed; (re)starting $SERVICE_USER"
	systemctl restart "$SERVICE_USER.service"
else
	# Nothing changed; start it if it isn't running (fix a stopped service).
	if ! systemctl is-active --quiet "$SERVICE_USER.service"; then
		echo "service not running; starting"
		systemctl start "$SERVICE_USER.service"
	else
		echo "nothing to do; service already running with current config"
	fi
fi

echo
echo "=== status ==="
systemctl --no-pager --full status "$SERVICE_USER.service" || true
echo
echo "subscribe URL:  ${PUBLIC_BASE_URL}/rss/untls?url=<encoded-https-feed>"
echo "config:         $ENV_FILE"
echo "unit:           $UNIT_FILE"
echo "binary:         $INSTALL_BIN"
