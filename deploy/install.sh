#!/usr/bin/env bash
# Install or upgrade superlight-uma on a systemd host (run as root).
#
#   sudo UMA_DOMAIN=uma.example.com UMA_APPS=my-app ./install.sh /path/to/superlight-uma [--caddy]
#
# Instance settings (environment variables, all optional; pass them after sudo):
#   UMA_DOMAIN  public domain for the Caddy site block (required with --caddy)
#   UMA_APPS    UMA_ALLOWED_APPS written on first install   (default: my-app)
#   UMA_CORS    UMA_CORS_ORIGINS written when missing       (default: https://*.pages.dev,http://localhost:8000)
#   UMA_PORT    local listen port                           (default: 8082)
#
# - creates system user superlight-uma (nologin)
# - installs the binary to /opt/superlight-uma/, keeping the old one as .prev
# - creates /etc/superlight-uma/superlight-uma.env on first install
#   (admin key generated locally with openssl, never printed); on upgrades only
#   adds missing settings (UMA_CORS_ORIGINS), never overwrites existing ones
# - installs/updates the systemd unit and (re)starts the service
# - with --caddy: appends a "$UMA_DOMAIN { reverse_proxy 127.0.0.1:$UMA_PORT }" block to /etc/caddy/Caddyfile
#   if missing (backup first), validates, and reloads Caddy (no restart)
#
# Rollback: sudo cp -a /opt/superlight-uma/superlight-uma.prev /opt/superlight-uma/superlight-uma \
#           && sudo systemctl restart superlight-uma
set -euo pipefail

BIN_SRC=${1:?usage: install.sh /path/to/superlight-uma [--caddy]}
WITH_CADDY=${2:-}
HERE=$(cd "$(dirname "$0")" && pwd)
APP=superlight-uma
OPT=/opt/$APP
ETC=/etc/$APP
ENV_FILE=$ETC/$APP.env
UNIT=/etc/systemd/system/$APP.service
CADDYFILE=/etc/caddy/Caddyfile
DOMAIN=${UMA_DOMAIN:-}
PORT=${UMA_PORT:-8082}
INIT_APPS=${UMA_APPS:-my-app}
# Origins allowed to call the API from a browser (dashboard on Cloudflare Pages + local testing).
DEFAULT_CORS=${UMA_CORS:-"https://*.pages.dev,http://localhost:8000"}

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
if [[ $WITH_CADDY == --caddy && -z $DOMAIN ]]; then
  echo "--caddy needs UMA_DOMAIN, e.g. sudo UMA_DOMAIN=uma.example.com $0 $BIN_SRC --caddy" >&2
  exit 1
fi

if ! id "$APP" >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$APP"
fi

install -d -m 0755 -o root -g root "$OPT"
if [[ -f $OPT/$APP ]]; then
  cp -a "$OPT/$APP" "$OPT/$APP.prev"
fi
install -m 0755 -o root -g root "$BIN_SRC" "$OPT/$APP.new"
mv -f "$OPT/$APP.new" "$OPT/$APP"

install -d -m 0750 -o root -g "$APP" "$ETC"
if [[ ! -f $ENV_FILE ]]; then
  umask 077
  {
    echo "UMA_ADDR=127.0.0.1:$PORT"
    echo "UMA_DB_PATH=/var/lib/$APP/uma.db"
    echo "UMA_ALLOWED_APPS=$INIT_APPS"
    echo "UMA_TRUST_PROXY=true"
    echo "UMA_CORS_ORIGINS=$DEFAULT_CORS"
    echo "UMA_ADMIN_KEY=$(openssl rand -hex 32)"
  } > "$ENV_FILE"
fi
# Upgrades: add settings introduced later without touching existing values.
if ! grep -q '^UMA_CORS_ORIGINS=' "$ENV_FILE"; then
  echo "UMA_CORS_ORIGINS=$DEFAULT_CORS" >> "$ENV_FILE"
fi
chown root:"$APP" "$ENV_FILE"
chmod 0640 "$ENV_FILE"

install -m 0644 -o root -g root "$HERE/$APP.service" "$UNIT"
systemctl daemon-reload
systemctl enable "$APP" >/dev/null 2>&1
systemctl restart "$APP"
sleep 1
systemctl is-active --quiet "$APP" || { journalctl -u "$APP" -n 30 --no-pager; exit 1; }
HEALTH_ADDR=$(sed -n 's/^UMA_ADDR=//p' "$ENV_FILE" | tail -1)
HEALTH_ADDR=${HEALTH_ADDR:-127.0.0.1:$PORT}
[[ $HEALTH_ADDR == :* ]] && HEALTH_ADDR=127.0.0.1$HEALTH_ADDR
curl -fsS "http://$HEALTH_ADDR/healthz" && echo

if [[ $WITH_CADDY == --caddy ]]; then
  if grep -qE "^[[:space:]]*$DOMAIN[[:space:]]*\{" "$CADDYFILE"; then
    echo "Caddy block for $DOMAIN already present; leaving Caddyfile unchanged"
  else
    BACKUP="$CADDYFILE.bak-$(date +%Y%m%d%H%M%S)"
    cp -a "$CADDYFILE" "$BACKUP"
    { echo; grep -v '^#' "$HERE/Caddyfile.snippet" | sed -e "s#uma.example.com#$DOMAIN#" -e "s#127.0.0.1:8082#127.0.0.1:$PORT#"; } >> "$CADDYFILE"
    if ! caddy validate --config "$CADDYFILE" --adapter caddyfile; then
      cp -a "$BACKUP" "$CADDYFILE"
      echo "caddy validate failed; Caddyfile restored from $BACKUP" >&2
      exit 1
    fi
    systemctl reload caddy
  fi
fi
echo "done: $("$OPT/$APP" -version)"
