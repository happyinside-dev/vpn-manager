#!/usr/bin/env bash
# Functions to merge into the existing proxy-setup.sh. This file is deliberately
# not a replacement installer: the original bootstrap remains responsible for
# Xray, Hysteria, SSH, UFW, BBR, TLS and server-level REALITY secrets.
set -Eeuo pipefail

PM_USER=proxy-manager
PM_GROUP=proxy-manager
PM_ETC=/etc/proxy-manager
PM_STATE=/var/lib/proxy-manager
PM_LOG=/var/log/proxy-manager
PM_BIN=/usr/local/bin/proxy-manager

backup_pm_inputs() {
  local stamp; stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  for f in /usr/local/etc/xray/config.json /etc/hysteria/config.yaml "$PM_STATE/proxy-manager.db"; do
    [[ -e "$f" ]] || continue
    install -m 600 "$f" "${f}.backup-${stamp}"
  done
  for f in /etc/hysteria/*.crt /etc/hysteria/*.pem /etc/hysteria/*.key; do
    [[ -e "$f" ]] || continue
    install -m 600 "$f" "${f}.backup-${stamp}"
  done
}

install_proxy_manager_files() {
  # $1 = already-built proxy-manager binary, $2 = generated config.yaml
  local src_bin="$1" src_cfg="$2"
  getent group "$PM_GROUP" >/dev/null || groupadd --system "$PM_GROUP"
  id "$PM_USER" >/dev/null 2>&1 || useradd --system --gid "$PM_GROUP" --home-dir "$PM_STATE" --shell /usr/sbin/nologin "$PM_USER"
  install -d -o root -g "$PM_GROUP" -m 0750 "$PM_ETC"
  install -d -o "$PM_USER" -g "$PM_GROUP" -m 0700 "$PM_STATE" "$PM_LOG"
  install -o root -g root -m 0755 "$src_bin" "$PM_BIN"
  install -o root -g "$PM_GROUP" -m 0640 "$src_cfg" "$PM_ETC/config.yaml"
  install -o root -g root -m 0644 deploy/proxy-manager.service /etc/systemd/system/proxy-manager.service
  systemctl daemon-reload
}

initialize_proxy_manager() {
  local admin_user="$1" admin_password="$2" password_file
  if [[ ! -s "$PM_STATE/proxy-manager.db" ]]; then
    password_file="$(mktemp "$PM_STATE/.admin-password.XXXXXX")"
    chown "$PM_USER:$PM_GROUP" "$password_file"
    chmod 0600 "$password_file"
    printf '%s' "$admin_password" >"$password_file"
    sudo -u "$PM_USER" "$PM_BIN" --config "$PM_ETC/config.yaml" init-admin --username "$admin_user" --password-file "$password_file"
    rm -f "$password_file"
  fi
  chown "$PM_USER:$PM_GROUP" "$PM_STATE/proxy-manager.db"* 2>/dev/null || true
  chmod 0600 "$PM_STATE/proxy-manager.db"* 2>/dev/null || true
}

migrate_legacy_if_needed() {
  # Call before replacing old Xray/Hysteria auth blocks. This command refuses
  # ambiguous/multi-user legacy configs and never overwrites a non-empty PM DB.
  if [[ ! -s "$PM_STATE/proxy-manager.db" ]] || ! sudo -u "$PM_USER" "$PM_BIN" --config "$PM_ETC/config.yaml" user list --json 2>/dev/null | grep -q '"id"'; then
    sudo -u "$PM_USER" "$PM_BIN" --config "$PM_ETC/config.yaml" migrate-legacy \
      --xray-config /usr/local/etc/xray/config.json \
      --hysteria-config /etc/hysteria/config.yaml || return 1
  fi
}

start_proxy_manager_stack() {
  systemctl enable proxy-manager.service
  systemctl restart proxy-manager.service
  systemctl restart xray.service
  systemctl restart hysteria-server.service
  "$PM_BIN" --config "$PM_ETC/config.yaml" reconcile
  curl --fail --silent --show-error http://127.0.0.1:18081/internal/health >/dev/null
  systemctl is-active --quiet proxy-manager.service
  systemctl is-active --quiet xray.service
  systemctl is-active --quiet hysteria-server.service
}

# REQUIRED integration order in proxy-setup.sh:
#   1. Parse -sni/-xsni/-hsni/-s exactly as before.
#   2. backup_pm_inputs
#   3. Install/update Xray and Hysteria, preserving REALITY private key and TLS cert/key when -s says so.
#   4. install_proxy_manager_files <binary> <config>, then initialize_proxy_manager <admin-user> <admin-password>.
#   5. If legacy credentials exist, invoke migrate_legacy_if_needed while the old configs are still intact.
#   6. Ensure Xray VLESS inbound tag is "vless-reality", remove the migrated legacy client from static clients, and add loopback API:
#        "api":{"tag":"api","listen":"127.0.0.1:10085","services":["HandlerService","StatsService"]}
#      Keep all server-level REALITY keys unchanged. The migrated UUID stays in SQLite and is re-added dynamically.
#   7. Configure Hysteria auth.type=http URL 127.0.0.1:18081/internal/hysteria/auth.
#   8. Set masquerade.proxy.url=https://$HY_SNI and rewriteHost=true.
#   9. start_proxy_manager_stack
#
# Never open TCP/18080, TCP/18081, or TCP/10085 in UFW. Xray remains TCP/443;
# Hysteria remains UDP/443. The panel is reached through SSH forwarding by default.
