#!/usr/bin/env bash
# proxy-setup.sh - VLESS/Reality (Xray) + Hysteria 2 behind a real website, in one command.
# Debian 11/12 (Ubuntu should also work). Run as root.
#
#   bash proxy-setup.sh
#
# How it works:
#   * Xray (Reality) listens on PORT/tcp. Anything that is NOT an authenticated Reality client
#     (scanners, active probes, browsers) is forwarded to a local nginx (127.0.0.1:8443)
#     that serves a real website over a valid Let's Encrypt certificate.
#   * Hysteria 2 listens on PORT/udp with the same kind of certificate; non-Hysteria HTTP/3
#     requests get the same website (masquerade).
#   * nginx also listens on 80/tcp (ACME challenges + redirect to https).
#
# SNI domains are chosen by you. Both must have an A record pointing to this server
# (DNS only, no CDN proxying). They may be the same name.
#
# Variables (all optional; you are asked for missing SNI values when a terminal is available):
#   XRAY_SNI=a.example.com   domain for Reality / nginx
#   HY_SNI=b.example.com     domain for Hysteria 2 (default: same as XRAY_SNI)
#   SITE_DIR=/path/to/site   copy this directory into the website root (/var/www/site)
#   LE_EMAIL=me@example.com  e-mail for Let's Encrypt (default: register without e-mail)
#   PORT=443                 port for both protocols (TCP for Reality, UDP for Hysteria 2)
#   SKIP_DNS_CHECK=1         do not verify that the domains resolve to this server
#   SKIP_FIREWALL=1          do not modify ufw
#   SSH_HARDEN=0             do not change SSH authentication settings
#   NEW_USER=proxy           admin (sudo) account to create; the password is prompted
#   NEW_USER_PASSWORD=       password for NEW_USER for non-interactive runs (avoid if possible)
#   SSH_USER=proxy           account that receives the SSH public key (default: NEW_USER)
#   SSH_PUBKEY="..."         provide the public key non-interactively
#
# Reinstall behavior:
#   * default: preserve accounts but rotate all VPN credentials (Reality keys, UUIDs, Hysteria passwords)
#   * -S / --preserve-secrets: preserve all existing VPN credentials during reinstall
# The Let's Encrypt certificate and website content are kept (Let's Encrypt rate limits).

set -Eeuo pipefail

# Remember which values were explicitly supplied by the caller before defaults/state loading.
REQUESTED_HY_INSECURE_SET=0
[[ -v HY_INSECURE ]] && REQUESTED_HY_INSECURE_SET=1
PORT="${PORT:-443}"
XRAY_VERSION_EXPLICIT=0
[[ -v XRAY_VERSION ]] && XRAY_VERSION_EXPLICIT=1
XRAY_VERSION="${XRAY_VERSION:-v26.6.27}"
XRAY_SNI="${XRAY_SNI:-}"
HY_SNI="${HY_SNI:-}"
HY_INSECURE="${HY_INSECURE:-0}"
REQUESTED_XRAY_SNI="$XRAY_SNI"
REQUESTED_HY_SNI="$HY_SNI"
REQUESTED_HY_INSECURE="$HY_INSECURE"
SITE_DIR="${SITE_DIR:-}"
LE_EMAIL="${LE_EMAIL:-}"
SKIP_DNS_CHECK="${SKIP_DNS_CHECK:-0}"
SKIP_FIREWALL="${SKIP_FIREWALL:-0}"
SSH_HARDEN="${SSH_HARDEN:-1}"
NEW_USER="${NEW_USER:-proxy}"
NEW_USER_PASSWORD="${NEW_USER_PASSWORD:-}"
SSH_USER="${SSH_USER:-$NEW_USER}"
SSH_PUBKEY="${SSH_PUBKEY:-}"
LINKS_FILE="${HOME:-/root}/proxy-links.txt"

XRAY_CONF=/usr/local/etc/xray/config.json
HY_DIR=/etc/hysteria
HY_CONF=$HY_DIR/config.yaml
BACKUP_ROOT=/var/backups/proxy-setup
STATE_DIR=/etc/proxy-manager
STATE_FILE=$STATE_DIR/state.json
XRAY_VERSION_PIN=$STATE_DIR/xray-version
MANAGER_BIN=/usr/local/sbin/proxy-manager
VPNM_BIN=${VPNM_BIN:-/usr/local/bin/vpn-manager}
LOG_DIR=/var/log/proxy-setup
DEFAULT_ACCOUNT="${DEFAULT_ACCOUNT:-default}"

SITE_ROOT=/var/www/site
ACME_ROOT=/var/www/letsencrypt
NGINX_DEST_PORT=8443
NGINX_HTTP_CONF=/etc/nginx/conf.d/proxy-http.conf
NGINX_TLS_CONF=/etc/nginx/conf.d/proxy-tls.conf
CERT_NAME=proxy-cert
LE_LIVE=/etc/letsencrypt/live/$CERT_NAME
CERT_HOOK=/etc/letsencrypt/renewal-hooks/deploy/proxy-setup.sh

c_g=$'\e[32m'; c_y=$'\e[33m'; c_r=$'\e[31m'; c_0=$'\e[0m'
log()  { echo "${c_g}[+]${c_0} $*" >&2; }
warn() { echo "${c_y}[!]${c_0} $*" >&2; }
die()  { echo "${c_r}[x]${c_0} $*" >&2; exit 1; }

# True only if /dev/tty can really be opened (the device node can exist without a controlling terminal,
# e.g. `ssh host 'bash script'` without -t).
have_tty() { { : </dev/tty >/dev/tty; } 2>/dev/null; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
trap 'die "Error on line $LINENO (command: $BASH_COMMAND)"' ERR

setup_run_log() {
  mkdir -p "$LOG_DIR"
  chmod 700 "$LOG_DIR"
  LOG_FILE="$LOG_DIR/proxy-setup-$(date +%Y%m%d-%H%M%S)-$$.log"
  touch "$LOG_FILE"
  chmod 600 "$LOG_FILE"
  exec > >(tee -a "$LOG_FILE") 2>&1
  log "Run log: $LOG_FILE"
}

show_help() {
  cat <<'EOF'
proxy-manager - Xray/REALITY + Hysteria 2 installer and account manager

INSTALL / REINSTALL:
  proxy-manager
  proxy-manager install
      Install on a new server. On an existing installation, keep account names
      and enabled/disabled state, but ROTATE all VPN credentials.

  proxy-manager -S
  proxy-manager install -S
  proxy-manager --preserve-secrets
      Reinstall/repair while preserving existing REALITY keys, account UUIDs,
      Hysteria passwords, account states and links.

ACCOUNT MANAGEMENT:
  proxy-manager account add NAME       Create and enable a new account
  proxy-manager account list           List all accounts
  proxy-manager account show NAME      Show links for an account
  proxy-manager account enable NAME    Enable an existing account
  proxy-manager account disable NAME   Disable without deleting credentials
  proxy-manager account rotate NAME    Rotate only this account's UUID/password
  proxy-manager account delete NAME    Permanently delete an account

CONFIGURATION / STATUS:
  proxy-manager enable-live-users      Enable live updates (one-time VPN restart)
  proxy-manager apply                  Regenerate server configs (restarts VPN)
  proxy-manager status                 Show services and port listeners
  proxy-manager self-test              Run protocol end-to-end tests locally
  proxy-manager xray version           Show installed/pinned Xray version
  proxy-manager xray test VERSION      Test one Xray version; rollback on failure
  proxy-manager xray matrix [VERSIONS] Test candidates and keep first passing version

HELP:
  proxy-manager -h
  proxy-manager --help
  proxy-manager help

INSTALL ENVIRONMENT VARIABLES:
  XRAY_SNI=domain.example              REALITY/nginx domain
  HY_SNI=domain.example                Hysteria TLS SNI (default: XRAY_SNI)
  HY_INSECURE=1                         Allow Hysteria TLS name/cert mismatch (for external SNI)
  SITE_DIR=/path/to/site               Static website source directory
  LE_EMAIL=mail@example.com            Let's Encrypt e-mail
  PORT=443                             TCP/UDP VPN port
  XRAY_VERSION=v26.6.27                Xray version (may also be pinned by xray matrix)
  SKIP_DNS_CHECK=1                     Skip A-record verification
  SKIP_FIREWALL=1                      Do not modify UFW
  SSH_HARDEN=0                         Do not change SSH authentication
  NEW_USER=proxy                       Admin account name
  NEW_USER_PASSWORD=...                Non-interactive admin password
  SSH_USER=proxy                       User receiving SSH public key
  SSH_PUBKEY='ssh-ed25519 ...'         Public key for non-interactive setup
  DEFAULT_ACCOUNT=default              Initial account name on first install

Examples:
  XRAY_SNI=vpn.example.com proxy-manager
  proxy-manager -S
  proxy-manager account add alice
  proxy-manager account disable alice
  proxy-manager account rotate alice
  proxy-manager self-test
EOF
}

# Help must work even without root privileges.
case "${1:-}" in
  -h|--help|help) show_help; exit 0 ;;
esac

# ---------- checks ----------
[[ $EUID -eq 0 ]] || die "Run this script as root (sudo -i)."
[[ -r /etc/os-release ]] && . /etc/os-release
[[ "${ID:-}" == debian || "${ID:-}" == ubuntu ]] || warn "This script targets Debian/Ubuntu. Detected: ${ID:-unknown}. Continuing anyway."
command -v systemctl >/dev/null || die "systemd is required."

# ---------- dependencies ----------
install_deps() {
  log "Installing dependencies..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq curl jq openssl ca-certificates ufw iproute2 coreutils openssh-client openssh-server sudo >/dev/null
}

install_web() {
  log "Installing nginx and certbot..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y -qq nginx certbot >/dev/null
  # The stock "welcome to nginx" site would shadow ours on port 80.
  rm -f /etc/nginx/sites-enabled/default
}

# ---------- public IP ----------
detect_ip() {
  local ip u
  for u in https://api.ipify.org https://ifconfig.me https://icanhazip.com; do
    ip=$(curl -4 -fsS --max-time 8 "$u" 2>/dev/null | tr -d '[:space:]') || continue
    [[ $ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && { echo "$ip"; return; }
  done
  die "Could not detect the public IPv4 address."
}

# ---------- user input: domains, e-mail, site ----------
valid_domain() {
  [[ $1 =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]]
}

check_dns() {
  local d ips v6

  # XRAY_SNI is the real website/certificate name and MUST resolve to this VPS.
  d="$XRAY_SNI"
  ips=$(getent ahostsv4 "$d" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ' || true)
  [[ -n $ips ]] || die "$d has no A record. Create an A record $d -> $PUBLIC_IP (DNS only, no proxying) and rerun."
  [[ " $ips " == *" $PUBLIC_IP "* ]] \
    || die "$d resolves to: $ips -- not to this server ($PUBLIC_IP). Fix the A record and wait for DNS to update (or SKIP_DNS_CHECK=1)."
  v6=$(getent ahostsv6 "$d" 2>/dev/null | awk '{print $1}' | grep -v '^::ffff:' | sort -u | tr '\n' ' ' || true)
  [[ -z $v6 ]] || warn "$d also has an AAAA record ($v6). Make sure it points to THIS server, or remove it; otherwise certificate validation and clients may fail."

  # Hysteria may intentionally use an external SNI such as bing.com with
  # certificate verification disabled. In that mode it must NOT resolve here.
  if [[ $HY_INSECURE != 1 && $HY_SNI != "$XRAY_SNI" ]]; then
    d="$HY_SNI"
    ips=$(getent ahostsv4 "$d" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ' || true)
    [[ -n $ips ]] || die "$d has no A record. For a local verified Hysteria SNI, point it to $PUBLIC_IP; for an external SNI use HY_INSECURE=1."
    [[ " $ips " == *" $PUBLIC_IP "* ]] \
      || die "$d does not resolve to this server. Use HY_INSECURE=1 for an external Hysteria SNI such as bing.com."
  fi
  log "DNS check passed."
}
collect_input() {
  local interactive=0
  have_tty && interactive=1

  if [[ -z $XRAY_SNI ]]; then
    (( interactive )) || die "XRAY_SNI is not set and there is no terminal. Example: XRAY_SNI=a.example.com HY_SNI=b.example.com bash $0"
    printf "SNI domain for Xray/Reality (A record must point to %s): " "$PUBLIC_IP" >/dev/tty
    IFS= read -r XRAY_SNI </dev/tty || die "Could not read the domain."
  fi
  if [[ -z $HY_SNI ]] && (( interactive )); then
    printf "SNI domain for Hysteria 2 (Enter = same as Xray): " >/dev/tty
    IFS= read -r HY_SNI </dev/tty || die "Could not read the domain."
  fi
  XRAY_SNI=$(printf '%s' "$XRAY_SNI" | tr -d '[:space:]' | tr 'A-Z' 'a-z')
  HY_SNI=$(printf '%s' "${HY_SNI:-$XRAY_SNI}" | tr -d '[:space:]' | tr 'A-Z' 'a-z')
  valid_domain "$XRAY_SNI" || die "Invalid Xray SNI domain: '$XRAY_SNI'"
  valid_domain "$HY_SNI"   || die "Invalid Hysteria SNI domain: '$HY_SNI'"

  [[ $HY_INSECURE == 0 || $HY_INSECURE == 1 ]] || die "HY_INSECURE must be 0 or 1."

  if [[ -z $LE_EMAIL ]] && (( interactive )); then
    printf "E-mail for Let's Encrypt notices (optional, Enter to skip): " >/dev/tty
    IFS= read -r LE_EMAIL </dev/tty || LE_EMAIL=""
  fi
  LE_EMAIL=$(printf '%s' "$LE_EMAIL" | tr -d '[:space:]')
  [[ -z $LE_EMAIL || $LE_EMAIL =~ ^[^@]+@[^@]+\.[^@]+$ ]] || die "Invalid e-mail: $LE_EMAIL"

  [[ -z $SITE_DIR || -d $SITE_DIR ]] || die "SITE_DIR is not a directory: $SITE_DIR"

  log "Xray SNI: $XRAY_SNI | Hysteria SNI: $HY_SNI | Hysteria insecure: $HY_INSECURE"
  if [[ $SKIP_DNS_CHECK == 1 ]]; then warn "DNS check skipped (SKIP_DNS_CHECK=1)."; else check_dns; fi
}

# ---------- install Xray and Hysteria 2 ----------
validate_xray_version() {
  [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || die "Invalid Xray version: $1 (expected e.g. v26.7.28)."
}

install_xray_release() {
  local version=$1 installed
  validate_xray_version "$version"
  log "Installing Xray $version..."
  bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ \
    install --version "$version" -f >/dev/null 2>&1 \
    || return 1
  command -v xray >/dev/null || return 1
  installed=$(xray version 2>/dev/null | awk 'NR==1 {print $2}')
  [[ $installed == "${version#v}" ]] || return 1
  log "Xray version verified: $installed"
}

install_xray() {
  # An explicit environment value wins. Otherwise use the compatibility pin
  # selected by `proxy-manager xray matrix`, if one exists.
  if [[ -z ${XRAY_VERSION_EXPLICIT:-} && -s $XRAY_VERSION_PIN ]]; then
    local pinned
    pinned=$(tr -d '[:space:]' <"$XRAY_VERSION_PIN")
    if [[ $pinned =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      XRAY_VERSION=$pinned
    fi
  fi
  log "Installing Xray $XRAY_VERSION (compatibility pin)..."
  install_xray_release "$XRAY_VERSION" || die "Failed to install Xray $XRAY_VERSION."
}

install_hysteria() {
  log "Installing Hysteria 2..."
  bash <(curl -fsSL https://get.hy2.sh/) >/dev/null 2>&1 || die "Failed to install Hysteria 2."
  command -v hysteria >/dev/null || die "hysteria was not found after installation."
}

stop_services() {
  systemctl stop xray hysteria-server 2>/dev/null || true
}

check_ports() {
  local busy p
  busy=$(ss -H -lntup "sport = :$PORT" 2>/dev/null || true)
  [[ -z $busy ]] || die "Port $PORT is already in use:"$'\n'"$busy"
  # 80 and the local nginx port may only be held by nginx itself (rerun).
  for p in 80 "$NGINX_DEST_PORT"; do
    busy=$(ss -H -ltnp "sport = :$p" 2>/dev/null | grep -v nginx || true)
    [[ -z $busy ]] || die "Port $p is already in use by another process:"$'\n'"$busy"
  done
}

# ---------- backup existing configuration ----------
backup_existing() {
  local ts backup_dir found=0 f
  ts=$(date +%Y%m%d-%H%M%S)
  backup_dir="$BACKUP_ROOT/$ts"

  local ssh_home ssh_auth_keys
  ssh_home=$(getent passwd "$SSH_USER" 2>/dev/null | cut -d: -f6 || true)
  ssh_auth_keys="${ssh_home:+$ssh_home/.ssh/authorized_keys}"

  for f in "$STATE_FILE" "$XRAY_CONF" "$HY_CONF" "$HY_DIR/server.key" "$HY_DIR/server.crt" "$LINKS_FILE" \
           "$NGINX_HTTP_CONF" "$NGINX_TLS_CONF" "$CERT_HOOK" \
           /etc/ssh/sshd_config /etc/ssh/sshd_config.d/00-proxy-setup-hardening.conf /etc/ssh/sshd_config.d/99-proxy-setup-hardening.conf ${ssh_auth_keys:+"$ssh_auth_keys"}; do
    [[ -e $f ]] || continue
    if (( found == 0 )); then
      mkdir -p "$backup_dir"
      chmod 700 "$BACKUP_ROOT" "$backup_dir"
      found=1
    fi

    # Preserve the original absolute path layout inside the backup directory.
    mkdir -p "$backup_dir$(dirname "$f")"
    cp -a -- "$f" "$backup_dir$f"
  done

  if (( found == 1 )); then
    log "Previous configuration backed up to: $backup_dir"
  else
    log "No existing configuration found. Backup is not required."
  fi
}

# ---------- admin user ----------
# Creates $NEW_USER with sudo (root) rights. The password is asked only when the
# user has to be created; if the user already exists it is left untouched
# (only sudo group membership is ensured).
create_admin_user() {
  [[ $NEW_USER =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "Invalid user name: $NEW_USER"

  if id "$NEW_USER" >/dev/null 2>&1; then
    usermod -aG sudo "$NEW_USER"
    log "User $NEW_USER already exists. Password left unchanged, sudo rights ensured."
    return
  fi

  local pass1="$NEW_USER_PASSWORD" pass2=""
  if [[ -z $pass1 ]]; then
    have_tty \
      || die "No interactive terminal to ask the password for $NEW_USER. Run interactively or set NEW_USER_PASSWORD."
    echo >/dev/tty
    echo "Creating admin user '$NEW_USER' (sudo rights). Choose a password for it." >/dev/tty
    while true; do
      printf "Password for %s: " "$NEW_USER" >/dev/tty
      IFS= read -r -s pass1 </dev/tty || die "Could not read the password."
      printf '\n' >/dev/tty
      printf "Repeat password: " >/dev/tty
      IFS= read -r -s pass2 </dev/tty || die "Could not read the password."
      printf '\n' >/dev/tty
      if [[ -z $pass1 ]]; then
        warn "The password must not be empty."
      elif [[ $pass1 != "$pass2" ]]; then
        warn "Passwords do not match. Try again."
      else
        break
      fi
    done
  fi

  getent group sudo >/dev/null || die "The sudo group does not exist (is the sudo package installed?)."
  useradd -m -s /bin/bash -G sudo "$NEW_USER"
  printf '%s:%s\n' "$NEW_USER" "$pass1" | chpasswd
  pass1=""; pass2=""; NEW_USER_PASSWORD=""
  log "User $NEW_USER created and added to the sudo group."
}

# ---------- SSH key authentication ----------
setup_ssh_key_auth() {
  if [[ $SSH_HARDEN == 0 ]]; then
    warn "SSH hardening skipped (SSH_HARDEN=0)."
    return
  fi

  id "$SSH_USER" >/dev/null 2>&1 || die "SSH user does not exist: $SSH_USER"
  command -v sshd >/dev/null || die "sshd is required for SSH hardening."
  command -v ssh-keygen >/dev/null || die "ssh-keygen is required for SSH key validation."

  local key="$SSH_PUBKEY" key_file home group auth_dir auth_file dropin
  home=$(getent passwd "$SSH_USER" | cut -d: -f6)
  group=$(id -gn "$SSH_USER")
  [[ -n $home && -d $home ]] || die "Could not determine a valid home directory for $SSH_USER."

  if [[ -z $key ]]; then
    if have_tty; then
      echo >&2
      echo "SSH password authentication will be disabled after the key is validated." >&2
      echo "Paste ONE public key for user '$SSH_USER'." >&2
      echo "Make sure you have the matching private key on your local machine." >&2
      printf "Public key: " >/dev/tty
      IFS= read -r key </dev/tty || die "Could not read the SSH public key."
    else
      warn "No interactive terminal and SSH_PUBKEY is empty. SSH hardening skipped."
      return
    fi
  fi

  [[ $key != *$'\n'* && $key != *$'\r'* ]] || die "SSH public key must be a single line."
  [[ $key =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521))[[:space:]]+[^[:space:]]+([[:space:]].*)?$ ]] \
    || die "Unsupported or malformed SSH public key."

  key_file="$WORK/ssh-public-key.pub"
  printf '%s\n' "$key" >"$key_file"
  ssh-keygen -l -f "$key_file" >/dev/null 2>&1 || die "ssh-keygen rejected the supplied public key."

  log "Validated SSH key: $(ssh-keygen -lf "$key_file" | awk '{print $2, $3}')"

  auth_dir="$home/.ssh"
  auth_file="$auth_dir/authorized_keys"
  install -d -m 700 -o "$SSH_USER" -g "$group" "$auth_dir"
  touch "$auth_file"
  chown "$SSH_USER:$group" "$auth_file"
  chmod 600 "$auth_file"

  if ! grep -qxF -- "$key" "$auth_file"; then
    printf '%s\n' "$key" >>"$auth_file"
  fi

  dropin=/etc/ssh/sshd_config.d/00-proxy-setup-hardening.conf
  mkdir -p /etc/ssh/sshd_config.d
  cat >"$dropin" <<'EOF'
# Managed by proxy-setup.sh
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
EOF
  chmod 644 "$dropin"

  if ! sshd -t; then
    rm -f "$dropin"
    die "sshd rejected the hardening config. Password authentication was not disabled."
  fi

  local effective
  effective=$(sshd -T 2>/dev/null) || {
    rm -f "$dropin"
    die "Could not read the effective sshd configuration. Password authentication was not disabled."
  }
  grep -qx 'pubkeyauthentication yes' <<<"$effective" || {
    rm -f "$dropin"
    die "Effective sshd config does not enable public key authentication."
  }
  grep -qx 'passwordauthentication no' <<<"$effective" || {
    rm -f "$dropin"
    die "Effective sshd config still allows password authentication."
  }
  grep -qx 'kbdinteractiveauthentication no' <<<"$effective" || {
    rm -f "$dropin"
    die "Effective sshd config still allows keyboard-interactive authentication."
  }

  if systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null; then
    rm -f /etc/ssh/sshd_config.d/99-proxy-setup-hardening.conf
    log "SSH key authentication configured for $SSH_USER. Password authentication is disabled."
  else
    rm -f "$dropin"
    systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
    die "Could not reload SSH. The hardening drop-in was removed."
  fi
}

# ---------- network and firewall ----------
tune_net() {
  modprobe tcp_bbr 2>/dev/null || true
  if grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null; then
    printf 'net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n' >/etc/sysctl.d/99-bbr.conf
    sysctl -q -p /etc/sysctl.d/99-bbr.conf >/dev/null 2>&1 || true
    log "BBR enabled."
  else
    warn "BBR is unavailable (container or unsupported kernel). Skipping."
  fi
}

setup_firewall() {
  if [[ $SKIP_FIREWALL == 1 ]]; then warn "Firewall setup skipped (SKIP_FIREWALL=1). Make sure 80/tcp, $PORT/tcp and $PORT/udp are open."; return; fi
  log "Configuring ufw..."
  local ports=() p
  mapfile -t ports < <(sshd -T 2>/dev/null | awk '/^port /{print $2}')
  [[ -n ${SSH_CONNECTION:-} ]] && ports+=("${SSH_CONNECTION##* }")  # port of the current SSH session
  [[ ${#ports[@]} -gt 0 ]] || ports=(22)

  if ! {
    ufw default deny incoming &&
    ufw default allow outgoing &&
    for p in $(printf '%s\n' "${ports[@]}" | sort -u); do ufw allow "$p/tcp"; done &&
    ufw allow 80/tcp &&
    ufw allow "$PORT/tcp" &&
    ufw allow "$PORT/udp" &&
    ufw --force enable
  } >/dev/null 2>&1; then
    warn "ufw could not be enabled (common on OpenVZ/LXC). Open 80/tcp, $PORT/tcp and $PORT/udp in the provider panel."
  else
    log "ufw enabled. Allowed: SSH ($(printf '%s ' "${ports[@]}" | xargs -n1 | sort -u | xargs)), 80/tcp, $PORT/tcp, $PORT/udp."
  fi
}

# ---------- website, nginx, certificate ----------
prepare_site() {
  mkdir -p "$SITE_ROOT" "$ACME_ROOT"
  if [[ -n $SITE_DIR ]]; then
    log "Copying website from $SITE_DIR to $SITE_ROOT..."
    cp -a "$SITE_DIR"/. "$SITE_ROOT"/
  fi
  if [[ -z $(ls -A "$SITE_ROOT" 2>/dev/null) ]]; then
    warn "No website supplied -- creating a minimal placeholder. Put a real site into $SITE_ROOT (or pass SITE_DIR=...)."
    cat >"$SITE_ROOT/index.html" <<'EOF'
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Welcome</title>
<style>body{font-family:system-ui,sans-serif;max-width:40rem;margin:20vh auto;padding:0 1rem;color:#222}h1{font-weight:600}</style>
</head>
<body>
<h1>Welcome</h1>
<p>This site is currently being set up. Please check back soon.</p>
</body>
</html>
EOF
  fi
  chmod -R a+rX "$SITE_ROOT" "$ACME_ROOT"
}

write_nginx_http() {
  local v6=""
  [[ -s /proc/net/if_inet6 ]] && v6="    listen [::]:80 default_server;"
  cat >"$NGINX_HTTP_CONF" <<EOF
# Managed by proxy-setup.sh -- port 80: ACME challenges and redirect to https.
server {
    listen 80 default_server;
$v6
    server_name _;
    server_tokens off;

    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        default_type text/plain;
    }
    location / {
        return 301 https://\$host\$request_uri;
    }
}
EOF
  nginx -t >/dev/null 2>"$WORK/nginx-t.log" || { cat "$WORK/nginx-t.log" >&2; rm -f "$NGINX_HTTP_CONF"; die "nginx rejected the port 80 config."; }
  systemctl enable nginx >/dev/null 2>&1 || true
  systemctl restart nginx
}

write_cert_hook() {
  mkdir -p "$(dirname "$CERT_HOOK")"
  cat >"$CERT_HOOK" <<EOF
#!/bin/sh
# Managed by proxy-setup.sh -- runs after every successful certificate renewal.
set -e
LIVE=$LE_LIVE
if id hysteria >/dev/null 2>&1 && [ -d $HY_DIR ]; then
  install -m 644 -o hysteria -g hysteria "\$LIVE/fullchain.pem" $HY_DIR/server.crt
  install -m 600 -o hysteria -g hysteria "\$LIVE/privkey.pem" $HY_DIR/server.key
  systemctl restart hysteria-server || true
fi
systemctl reload nginx || true
EOF
  chmod 755 "$CERT_HOOK"
}

obtain_cert() {
  log "Requesting a Let's Encrypt certificate (this needs port 80 reachable from the internet)..."
  local -a args=(certonly --webroot -w "$ACME_ROOT" --cert-name "$CERT_NAME"
                 --non-interactive --agree-tos --keep-until-expiring --expand --no-directory-hooks
                 -d "$XRAY_SNI")
  if [[ $HY_INSECURE != 1 && $HY_SNI != "$XRAY_SNI" ]]; then args+=(-d "$HY_SNI"); fi
  if [[ -n $LE_EMAIL ]]; then args+=(-m "$LE_EMAIL"); else args+=(--register-unsafely-without-email); fi

  if ! certbot "${args[@]}" >"$WORK/certbot.log" 2>&1; then
    tail -n 25 "$WORK/certbot.log" >&2
    die "certbot failed (see above). Check that port 80 is reachable from the internet and that the DNS records point to $PUBLIC_IP."
  fi
  [[ -s $LE_LIVE/fullchain.pem && -s $LE_LIVE/privkey.pem ]] || die "Certificate files were not created in $LE_LIVE."
  systemctl enable --now certbot.timer >/dev/null 2>&1 || true
  log "Certificate ready: $LE_LIVE"
}

write_nginx_tls() {
  log "Writing nginx TLS site (127.0.0.1:$NGINX_DEST_PORT)..."
  cat >"$NGINX_TLS_CONF" <<EOF
# Managed by proxy-setup.sh -- the site Xray (Reality) falls back to for non-Reality traffic.
server {
    listen 127.0.0.1:$NGINX_DEST_PORT ssl http2;
    server_name $XRAY_SNI;
    server_tokens off;

    ssl_certificate     $LE_LIVE/fullchain.pem;
    ssl_certificate_key $LE_LIVE/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;

    root $SITE_ROOT;
    index index.html index.htm;

    location / {
        try_files \$uri \$uri/ =404;
    }
}
EOF
  nginx -t >/dev/null 2>"$WORK/nginx-t.log" || { cat "$WORK/nginx-t.log" >&2; rm -f "$NGINX_TLS_CONF"; die "nginx rejected the TLS site config."; }
  systemctl reload nginx
}

# ---------- persistent state and account credentials ----------
valid_account_name() {
  [[ $1 =~ ^[a-z0-9][a-z0-9._-]{0,31}$ ]]
}

ensure_state_dir() {
  install -d -m 700 -o root -g root "$STATE_DIR"
}

gen_uuid() { cat /proc/sys/kernel/random/uuid; }
gen_password() { openssl rand -hex 16; }

gen_server_secrets() {
  log "Generating server credentials..."
  local kp
  kp=$(xray x25519 2>&1)
  PRIV=$(grep -iE '^private' <<<"$kp" | awk '{print $NF}')
  PUB=$(grep -iE '^(public|password)' <<<"$kp" | awk '{print $NF}')
  [[ -n $PRIV && -n $PUB ]] || die "Could not parse xray x25519 output:
$kp"
  SERVER_SHORT_ID=$(openssl rand -hex 8)
  SENTINEL_UUID=$(gen_uuid)
  SENTINEL_HY_PASS=$(gen_password)
}

create_initial_state() {
  ensure_state_dir
  valid_account_name "$DEFAULT_ACCOUNT" || die "Invalid DEFAULT_ACCOUNT: $DEFAULT_ACCOUNT"

  local uuid hy_pass now
  uuid=$(gen_uuid)
  hy_pass=$(gen_password)
  now=$(date -Is)

  jq -n \
    --argjson version 2 \
    --arg public_ip "$PUBLIC_IP" \
    --argjson port "$PORT" \
    --arg xray_sni "$XRAY_SNI" \
    --arg hy_sni "$HY_SNI" \
    --argjson hy_insecure "$HY_INSECURE" \
    --arg priv "$PRIV" \
    --arg pub "$PUB" \
    --arg sid "$SERVER_SHORT_ID" \
    --arg sentinel_uuid "$SENTINEL_UUID" \
    --arg sentinel_hy_pass "$SENTINEL_HY_PASS" \
    --arg name "$DEFAULT_ACCOUNT" \
    --arg uuid "$uuid" \
    --arg hy_pass "$hy_pass" \
    --arg now "$now" \
    '{
      version: $version,
      server: {
        public_ip: $public_ip,
        port: $port,
        xray_sni: $xray_sni,
        hy_sni: $hy_sni,
        hy_insecure: $hy_insecure,
        reality_private_key: $priv,
        reality_public_key: $pub,
        reality_short_id: $sid,
        sentinel_uuid: $sentinel_uuid,
        sentinel_hy_password: $sentinel_hy_pass
      },
      accounts: {
        ($name): {
          name: $name,
          enabled: true,
          xray_uuid: $uuid,
          hysteria_password: $hy_pass,
          created_at: $now,
          updated_at: $now
        }
      }
    }' >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp"
  mv "$STATE_FILE.tmp" "$STATE_FILE"
  chown root:root "$STATE_FILE"
}

load_state() {
  [[ -s $STATE_FILE ]] || die "State file not found: $STATE_FILE. Run installation first."
  jq -e '.version >= 2 and .server and .accounts' "$STATE_FILE" >/dev/null \
    || die "Unsupported or corrupt state file: $STATE_FILE"

  PUBLIC_IP=$(jq -r '.server.public_ip' "$STATE_FILE")
  PORT=$(jq -r '.server.port' "$STATE_FILE")
  XRAY_SNI=$(jq -r '.server.xray_sni' "$STATE_FILE")
  HY_SNI=$(jq -r '.server.hy_sni' "$STATE_FILE")
  HY_INSECURE=$(jq -r '.server.hy_insecure // false | if . then 1 else 0 end' "$STATE_FILE")
  PRIV=$(jq -r '.server.reality_private_key' "$STATE_FILE")
  PUB=$(jq -r '.server.reality_public_key' "$STATE_FILE")
  SERVER_SHORT_ID=$(jq -r '.server.reality_short_id' "$STATE_FILE")
  SENTINEL_UUID=$(jq -r '.server.sentinel_uuid' "$STATE_FILE")
  SENTINEL_HY_PASS=$(jq -r '.server.sentinel_hy_password' "$STATE_FILE")
}

update_server_state() {
  local now
  now=$(date -Is)
  jq --arg ip "$PUBLIC_IP" --argjson port "$PORT" --arg xs "$XRAY_SNI" --arg hs "$HY_SNI" --argjson hi "$HY_INSECURE" --arg now "$now" \
    '.server.public_ip=$ip | .server.port=$port | .server.xray_sni=$xs | .server.hy_sni=$hs | .server.hy_insecure=($hi == 1) | .server.updated_at=$now' \
    "$STATE_FILE" >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
}

rotate_all_credentials() {
  local kp priv pub sid sentinel_uuid sentinel_hy now name uuid pass tmp
  log "Rotating REALITY keys and credentials for all accounts..."

  kp=$(xray x25519 2>&1)
  priv=$(grep -iE '^private' <<<"$kp" | awk '{print $NF}')
  pub=$(grep -iE '^(public|password)' <<<"$kp" | awk '{print $NF}')
  [[ -n $priv && -n $pub ]] || die "Could not parse xray x25519 output:
$kp"
  sid=$(openssl rand -hex 8)
  sentinel_uuid=$(gen_uuid)
  sentinel_hy=$(gen_password)
  now=$(date -Is)

  tmp="$STATE_FILE.rotate.tmp"
  jq \
    --arg priv "$priv" --arg pub "$pub" --arg sid "$sid" \
    --arg su "$sentinel_uuid" --arg sh "$sentinel_hy" --arg now "$now" \
    '.server.reality_private_key=$priv
     | .server.reality_public_key=$pub
     | .server.reality_short_id=$sid
     | .server.sentinel_uuid=$su
     | .server.sentinel_hy_password=$sh
     | .server.credentials_rotated_at=$now' \
    "$STATE_FILE" >"$tmp"

  while IFS= read -r name; do
    uuid=$(gen_uuid)
    pass=$(gen_password)
    jq --arg n "$name" --arg u "$uuid" --arg p "$pass" --arg now "$now" \
      '.accounts[$n].xray_uuid=$u
       | .accounts[$n].hysteria_password=$p
       | .accounts[$n].updated_at=$now' \
      "$tmp" >"$tmp.next"
    mv "$tmp.next" "$tmp"
  done < <(jq -r '.accounts | keys[]' "$STATE_FILE")

  chmod 600 "$tmp"
  mv "$tmp" "$STATE_FILE"
  chown root:root "$STATE_FILE"
}

account_exists() {
  jq -e --arg n "$1" '.accounts[$n] != null' "$STATE_FILE" >/dev/null
}

account_add_state() {
  local name=$1 uuid pass now
  valid_account_name "$name" || die "Invalid account name '$name'. Use lowercase letters, digits, dot, underscore or dash (max 32 chars)."
  account_exists "$name" && die "Account already exists: $name"
  uuid=$(gen_uuid); pass=$(gen_password); now=$(date -Is)
  jq --arg n "$name" --arg u "$uuid" --arg p "$pass" --arg now "$now" \
    '.accounts[$n] = {name:$n, enabled:true, xray_uuid:$u, hysteria_password:$p, created_at:$now, updated_at:$now}' \
    "$STATE_FILE" >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
}

account_set_enabled_state() {
  local name=$1 enabled=$2 now
  account_exists "$name" || die "Account not found: $name"
  now=$(date -Is)
  jq --arg n "$name" --argjson enabled "$enabled" --arg now "$now" \
    '.accounts[$n].enabled=$enabled | .accounts[$n].updated_at=$now' "$STATE_FILE" >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
}

account_rotate_state() {
  local name=$1 uuid pass now
  account_exists "$name" || die "Account not found: $name"
  uuid=$(gen_uuid); pass=$(gen_password); now=$(date -Is)
  jq --arg n "$name" --arg u "$uuid" --arg p "$pass" --arg now "$now" \
    '.accounts[$n].xray_uuid=$u | .accounts[$n].hysteria_password=$p | .accounts[$n].updated_at=$now' \
    "$STATE_FILE" >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
}

account_delete_state() {
  local name=$1
  account_exists "$name" || die "Account not found: $name"
  jq --arg n "$name" 'del(.accounts[$n])' "$STATE_FILE" >"$STATE_FILE.tmp"
  chmod 600 "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
}

# ---------- proxy configuration ----------
write_xray() {
  log "Writing Xray configuration from account state..."
  mkdir -p "$(dirname "$XRAY_CONF")" || return 1
  local destination="$XRAY_CONF"
  local XRAY_CONF
  XRAY_CONF=$(mktemp "$(dirname "$destination")/.xray.XXXXXX.json") || return 1
  local clients
  clients=$(jq -c --arg sentinel "$SENTINEL_UUID" '
    ([.accounts[] | select(.enabled == true) | {id:.xray_uuid, email:.name, flow:"xtls-rprx-vision"}]
      + [{id:$sentinel, email:"__sentinel__", flow:"xtls-rprx-vision"}])' "$STATE_FILE") || return 1

  jq -n \
    --argjson port "$PORT" \
    --argjson clients "$clients" \
    --arg target "127.0.0.1:$NGINX_DEST_PORT" \
    --arg sni "$XRAY_SNI" \
    --arg priv "$PRIV" \
    --arg sid "$SERVER_SHORT_ID" \
    '{
      log:{loglevel:"warning"},
      api:{tag:"api",listen:"127.0.0.1:10085",services:["StatsService","HandlerService"]},
      stats:{},
      policy:{levels:{"0":{statsUserUplink:true,statsUserDownlink:true}}},
      inbounds:[{
        tag:"vless-reality",
        listen:"0.0.0.0",
        port:$port,
        protocol:"vless",
        settings:{users:$clients,decryption:"none"},
        streamSettings:{
          network:"tcp",
          security:"reality",
          realitySettings:{
            show:false,
            target:$target,
            xver:0,
            serverNames:[$sni],
            privateKey:$priv,
            shortIds:[$sid]
          }
        },
        sniffing:{enabled:true,destOverride:["http","tls","quic"]}
      }],
      outbounds:[
        {protocol:"freedom",tag:"direct"},
        {protocol:"blackhole",tag:"block"}
      ]
    }' >"$XRAY_CONF" || return 1

  local svc_user svc_group
  svc_user=$(systemctl show -p User --value xray 2>/dev/null || true)
  if [[ -n $svc_user && $svc_user != root ]] && id "$svc_user" >/dev/null 2>&1; then
    svc_group=$(id -gn "$svc_user")
    chown "root:$svc_group" "$XRAY_CONF" || return 1
    chmod 640 "$XRAY_CONF" || return 1
  else
    chown root:root "$XRAY_CONF" || return 1
    chmod 600 "$XRAY_CONF" || return 1
  fi
  xray run -test -config "$XRAY_CONF" >/dev/null 2>&1 || { warn "Xray rejected the generated config."; return 1; }
  mv -f -- "$XRAY_CONF" "$destination"
}

write_hysteria() {
  log "Writing Hysteria 2 configuration from account state..."
  mkdir -p "$HY_DIR"
  install -m 644 "$LE_LIVE/fullchain.pem" "$HY_DIR/server.crt"
  install -m 600 "$LE_LIVE/privkey.pem" "$HY_DIR/server.key"

  {
    echo "listen: :$PORT"
    echo
    echo "tls:"
    echo "  cert: $HY_DIR/server.crt"
    echo "  key: $HY_DIR/server.key"
    if [[ $HY_INSECURE == 1 ]]; then
      # External/spoofed SNI (for example bing.com) does not match the
      # certificate SAN. Disable Hysteria's server-side SNI guard only in
      # this explicit insecure mode; otherwise the TLS handshake is rejected.
      echo "  sniGuard: disable"
    fi
    echo
    echo "auth:"
    if [[ -f $STATE_DIR/live-users ]]; then
      echo "  type: http"
      echo "  http:"
      echo "    url: http://127.0.0.1:18081/auth"
    else
      echo "  type: userpass"
      echo "  userpass:"
      printf '    __sentinel__: %s\n' "$SENTINEL_HY_PASS"
      jq -r '.accounts[] | select(.enabled == true) | "    " + .name + ": " + .hysteria_password' "$STATE_FILE"
    fi
    echo
    echo "trafficStats:"
    echo "  listen: 127.0.0.1:10086"
    echo
    echo "masquerade:"
    echo "  type: file"
    echo "  file:"
    echo "    dir: $SITE_ROOT"
  } >"$HY_CONF"

  if id hysteria >/dev/null 2>&1; then
    chown hysteria:hysteria "$HY_CONF" "$HY_DIR/server.crt" "$HY_DIR/server.key"
  fi
  chmod 600 "$HY_CONF" "$HY_DIR/server.key"
  chmod 644 "$HY_DIR/server.crt"
}

start_services() {
  log "Starting services..."
  systemctl daemon-reload
  systemctl enable nginx xray hysteria-server >/dev/null 2>&1
  systemctl restart nginx
  systemctl restart xray
  systemctl restart hysteria-server
  sleep 2
  local s
  for s in nginx xray hysteria-server; do
    if ! systemctl is-active --quiet "$s"; then
      journalctl -u "$s" -n 20 --no-pager >&2 || true
      die "Service $s failed to start (see logs above)."
    fi
  done
}

# ---------- self-test ----------
SELFTEST_OK=1

find_free_tcp_port() {
  local p
  for p in $(shuf -i 18080-18999 -n 100); do
    if ! ss -H -ltn "sport = :$p" 2>/dev/null | grep -q .; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

wait_for_tcp_listener() {
  local port=$1 pid=$2 tries=${3:-50}
  local i
  for ((i=0; i<tries; i++)); do
    kill -0 "$pid" 2>/dev/null || return 1
    ss -H -ltn "sport = :$port" 2>/dev/null | grep -q . && return 0
    sleep 0.1
  done
  return 1
}

socks_exit_test() {
  local port=$1 out url
  for url in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    out=$(curl --socks5-hostname "127.0.0.1:$port" -4 -fsS --max-time 12 "$url" 2>/dev/null | tr -d '[:space:]' || true)
    [[ $out =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && {
      echo "$out"
      return 0
    }
  done
  return 1
}

self_test_xray_e2e() {
  local cfg="$WORK/xray-selftest.json" logf="$WORK/xray-selftest.log"
  local socks_port pid exit_ip
  socks_port=$(find_free_tcp_port) || { warn "Xray E2E: could not allocate a local test port."; return 1; }

  jq -n \
    --argjson socks_port "$socks_port" \
    --argjson server_port "$PORT" \
    --arg uuid "$SENTINEL_UUID" \
    --arg sni "$XRAY_SNI" \
    --arg pub "$PUB" \
    --arg sid "$SERVER_SHORT_ID" \
    '{
      log:{loglevel:"warning"},
      inbounds:[{
        listen:"127.0.0.1",
        port:$socks_port,
        protocol:"socks",
        settings:{udp:false}
      }],
      outbounds:[{
        protocol:"vless",
        settings:{
          address:"127.0.0.1",
          port:$server_port,
          id:$uuid,
          encryption:"none",
          flow:"xtls-rprx-vision"
        },
        streamSettings:{
          network:"tcp",
          security:"reality",
          realitySettings:{
            serverName:$sni,
            fingerprint:"chrome",
            password:$pub,
            shortId:$sid,
            spiderX:"/"
          }
        }
      }]
    }' >"$cfg"
  chmod 600 "$cfg"

  if ! xray run -test -config "$cfg" >"$logf" 2>&1; then
    warn "Xray E2E: generated client config was rejected: $(tail -n 3 "$logf" | tr '\n' ' ' | head -c 300)"
    return 1
  fi

  xray run -config "$cfg" >"$logf" 2>&1 &
  pid=$!
  if ! wait_for_tcp_listener "$socks_port" "$pid"; then
    kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true
    warn "Xray E2E: temporary client did not start: $(tail -n 5 "$logf" | tr '\n' ' ' | head -c 400)"
    return 1
  fi

  exit_ip=$(socks_exit_test "$socks_port" || true)
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  if [[ -n $exit_ip ]]; then
    log "Xray E2E passed: authenticated VLESS/REALITY tunnel reached the internet (exit $exit_ip)."
    return 0
  fi

  warn "Xray E2E failed: client started but traffic did not pass through the tunnel. Client log: $(tail -n 6 "$logf" | tr '\n' ' ' | head -c 500)"
  return 1
}

self_test_hysteria_e2e() {
  local cfg="$WORK/hysteria-selftest.yaml" logf="$WORK/hysteria-selftest.log"
  local socks_port pid exit_ip
  socks_port=$(find_free_tcp_port) || { warn "Hysteria E2E: could not allocate a local test port."; return 1; }

  cat >"$cfg" <<EOF
server: 127.0.0.1:$PORT
auth: "__sentinel__:$SENTINEL_HY_PASS"

tls:
  sni: $HY_SNI
  insecure: $([[ $HY_INSECURE == 1 ]] && echo true || echo false)

socks5:
  listen: 127.0.0.1:$socks_port
  disableUDP: true
EOF
  chmod 600 "$cfg"

  hysteria -c "$cfg" >"$logf" 2>&1 &
  pid=$!
  if ! wait_for_tcp_listener "$socks_port" "$pid" 80; then
    kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true
    warn "Hysteria E2E: temporary client did not connect/start: $(tail -n 6 "$logf" | tr '\n' ' ' | head -c 500)"
    return 1
  fi

  exit_ip=$(socks_exit_test "$socks_port" || true)
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  if [[ -n $exit_ip ]]; then
    log "Hysteria E2E passed: authenticated Hysteria2 tunnel reached the internet (exit $exit_ip)."
    return 0
  fi

  warn "Hysteria E2E failed: client started but traffic did not pass through the tunnel. Client log: $(tail -n 6 "$logf" | tr '\n' ' ' | head -c 500)"
  return 1
}

self_test() {
  SELFTEST_OK=1
  log "Running self-test..."
  local code listeners

  # 1) the site as served by nginx directly
  code=$(curl --noproxy '*' -sS --max-time 10 --resolve "$XRAY_SNI:$NGINX_DEST_PORT:127.0.0.1" \
         "https://$XRAY_SNI:$NGINX_DEST_PORT/" -o /dev/null -w '%{http_code}' 2>"$WORK/t1.err" || true)
  if [[ $code =~ ^[23] ]]; then
    log "nginx serves the site over a valid certificate (HTTP $code)."
  else
    warn "nginx check failed (HTTP ${code:-none}): $(head -c 200 "$WORK/t1.err" 2>/dev/null)"; SELFTEST_OK=0
  fi

  # 2) a NON-Reality client on the public TCP port must end up on nginx.
  code=$(curl --noproxy '*' -sS --max-time 10 --resolve "$XRAY_SNI:$PORT:127.0.0.1" \
         "https://$XRAY_SNI:$PORT/" -o /dev/null -w '%{http_code}' 2>"$WORK/t2.err" || true)
  if [[ $code =~ ^[23] ]]; then
    log "Xray fallback works: a plain HTTPS request on $PORT/tcp shows the site (HTTP $code)."
  else
    warn "Xray fallback check failed (HTTP ${code:-none}): $(head -c 200 "$WORK/t2.err" 2>/dev/null)"; SELFTEST_OK=0
  fi

  # 3) service listeners.
  listeners=$(ss -H -ltnp "sport = :$PORT" 2>/dev/null || true)
  grep -q xray <<<"$listeners" || { warn "xray is not listening on $PORT/tcp."; SELFTEST_OK=0; }
  listeners=$(ss -H -lunp "sport = :$PORT" 2>/dev/null || true)
  grep -q hysteria <<<"$listeners" || { warn "hysteria is not listening on $PORT/udp."; SELFTEST_OK=0; }

  # 4) actual authenticated protocol handshakes + proxied Internet request.
  self_test_xray_e2e || SELFTEST_OK=0
  self_test_hysteria_e2e || SELFTEST_OK=0

  if (( SELFTEST_OK )); then
    log "Self-test passed: fallback, listeners, VLESS/REALITY and Hysteria2 are working locally end-to-end."
    warn "This local E2E test cannot prove that an external provider firewall allows inbound TCP/UDP $PORT."
    return 0
  fi

  warn "Self-test reported problems (see above). The generated links may not work."
  return 1
}

# ---------- output / account manager ----------
account_links() {
  local name=$1 uuid pass enabled vless hy2
  account_exists "$name" || die "Account not found: $name"
  uuid=$(jq -r --arg n "$name" '.accounts[$n].xray_uuid' "$STATE_FILE")
  pass=$(jq -r --arg n "$name" '.accounts[$n].hysteria_password' "$STATE_FILE")
  enabled=$(jq -r --arg n "$name" '.accounts[$n].enabled' "$STATE_FILE")
  vless="vless://${uuid}@${PUBLIC_IP}:${PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${XRAY_SNI}&fp=chrome&pbk=${PUB}&sid=${SERVER_SHORT_ID}&type=tcp&headerType=none#Reality-${name}"
  hy2="hysteria2://${name}:${pass}@${PUBLIC_IP}:${PORT}/?sni=${HY_SNI}$([[ $HY_INSECURE == 1 ]] && printf '&insecure=1')#Hy2-${name}"

  echo "Account: $name"
  echo "Status: $([[ $enabled == true ]] && echo enabled || echo disabled)"
  echo
  echo "VLESS + Reality (TCP/$PORT):"
  echo "$vless"
  echo
  echo "Hysteria 2 (UDP/$PORT):"
  echo "$hy2"
}

print_links() {
  umask 077
  {
    echo "# Managed by proxy-manager"
    echo "# Xray SNI: $XRAY_SNI | Hysteria SNI: $HY_SNI"
    echo "# Generated: $(date -Is)"
    echo
    while IFS= read -r name; do
      account_links "$name"
      echo
    done < <(jq -r '.accounts | keys[]' "$STATE_FILE")
  } >"$LINKS_FILE"

  if account_exists "$DEFAULT_ACCOUNT"; then
    account_links "$DEFAULT_ACCOUNT"
  else
    local first
    first=$(jq -r '.accounts | keys[0] // empty' "$STATE_FILE")
    if [[ -n $first ]]; then
      account_links "$first"
    else
      echo "No user accounts exist. Add one with: proxy-manager account add NAME"
    fi
  fi
  echo
  echo "All account links saved to: $LINKS_FILE"
  echo "Manager installed as: $MANAGER_BIN"
}

backup_manager_state() {
  local ts dir
  ts=$(date +%Y%m%d-%H%M%S)
  dir="$BACKUP_ROOT/accounts-$ts-$$"
  mkdir -p "$dir"
  chmod 700 "$BACKUP_ROOT" "$dir"
  cp -a "$STATE_FILE" "$dir/state.json"
  [[ -e $XRAY_CONF ]] && cp -a "$XRAY_CONF" "$dir/xray-config.json"
  [[ -e $HY_CONF ]] && cp -a "$HY_CONF" "$dir/hysteria-config.yaml"
  echo "$dir"
}

apply_account_state() {
  load_state
  if [[ -f $STATE_DIR/live-users ]]; then write_auth_snapshot || return 1; fi
  write_xray
  write_hysteria
  systemctl restart xray
  systemctl restart hysteria-server
  systemctl is-active --quiet xray || return 1
  systemctl is-active --quiet hysteria-server || return 1
}

# ---------- live account provisioning ----------
live_users_ready() {
  [[ -f $STATE_DIR/live-users ]] &&
    jq -e '.api.services | index("HandlerService") != null' "$XRAY_CONF" >/dev/null &&
    jq -e '.inbounds[] | select(.tag == "vless-reality")' "$XRAY_CONF" >/dev/null &&
    grep -q 'url: http://127.0.0.1:18081/auth' "$HY_CONF" &&
    curl -fsS --max-time 3 http://127.0.0.1:18081/health >/dev/null
}

write_auth_snapshot() {
  local tmp
  tmp=$(mktemp "$HY_DIR/.auth-users.XXXXXX") || return 1
  jq '([.accounts[] | select(.enabled) | {key:.name,value:.hysteria_password}] +
       [{key:"__sentinel__",value:.server.sentinel_hy_password}]) | from_entries' "$STATE_FILE" >"$tmp" || return 1
  chown root:vpn-manager "$tmp" || return 1
  chmod 640 "$tmp" || return 1
  mv "$tmp" "$HY_DIR/auth-users.json"
}

# Xray CLI may exit zero on individual RPC failures. Verify its success count.
# Never print its output: RPC diagnostics can contain credentials.
live_xray_remove() {
  local result
  result=$(xray api rmu --server=127.0.0.1:10085 -timeout=5 -tag=vless-reality "$1" 2>&1) || return 1
  [[ $result == *'Removed 1 user(s) in total.'* ]] ||
    [[ $result == *"User $1 not found."* && $result == *'Removed 0 user(s) in total.'* ]]
}

live_xray_add() {
  local name=$1 result cfg="$WORK/live-add.json"
  jq --arg n "$name" '{inbounds:[{tag:"vless-reality",port:.server.port,protocol:"vless",
    settings:{decryption:"none",clients:[{email:$n,id:.accounts[$n].xray_uuid,flow:"xtls-rprx-vision"}]}}]}' "$STATE_FILE" >"$cfg" || return 1
  chmod 600 "$cfg"
  result=$(xray api adu --server=127.0.0.1:10085 -timeout=5 "$cfg" 2>&1) || return 1
  [[ $result == *'Added 1 user(s) in total.'* ]]
}

apply_live_account() {
  local name=$1
  # Remove only the target account. This also makes retries/rollback idempotent.
  live_xray_remove "$name" || return 1
  if jq -e --arg n "$name" '.accounts[$n].enabled == true' "$STATE_FILE" >/dev/null; then
    live_xray_add "$name" || return 1
  fi
  write_auth_snapshot || return 1
  # Deny future Hysteria authentication before disconnecting existing sessions.
  # Rotation also disconnects only this account, so old sessions cannot survive it.
  jq -cn --arg n "$name" '[$n]' >"$WORK/kick.json"
  curl -fsS --max-time 5 -H 'Content-Type: application/json' \
    --data-binary @"$WORK/kick.json" http://127.0.0.1:10086/kick >/dev/null || return 1
  # Persist the new client list for independent Xray restarts. No daemon reload.
  write_xray || return 1
  "$VPNM_BIN" sync-users --state "$STATE_FILE" --users-config /etc/vpn-manager/users.yaml >/dev/null
}

# One-time, explicit migration. Existing secrets and subscriptions are preserved.
enable_live_users() {
  if live_users_ready; then
    log "Live provisioning is already enabled."
    return
  fi
  [[ -x $VPNM_BIN ]] || die "Install the subscription backend first."
  timeout 5 "$VPNM_BIN" live-auth --help >/dev/null 2>&1 || die "Update vpn-manager binary first."
  local backup was_live=0
  [[ ! -f $STATE_DIR/live-users ]] || was_live=1
  backup=$(backup_manager_state)
  install -d -m 755 -o root -g root "$HY_DIR"
  write_auth_snapshot
  cat >/etc/systemd/system/proxy-manager-auth.service <<'EOAUTH'
[Unit]
Description=Local Hysteria account authentication
After=network.target
Before=hysteria-server.service
[Service]
User=vpn-manager
Group=vpn-manager
ExecStart=/usr/local/bin/vpn-manager live-auth
Restart=on-failure
RestartSec=1
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
EOAUTH
  cat >/etc/systemd/system/proxy-manager-recover.service <<'EORECOVER'
[Unit]
Description=Recover interrupted VPN account update
After=xray.service proxy-manager-auth.service hysteria-server.service
[Service]
Type=oneshot
ExecStart=/usr/local/libexec/proxy-manager-core recover-live-users
EORECOVER
  cat >/etc/systemd/system/proxy-manager-recover.timer <<'EOTIMER'
[Unit]
Description=Retry interrupted VPN account updates
[Timer]
OnBootSec=30s
OnUnitActiveSec=30s
[Install]
WantedBy=timers.target
EOTIMER
  systemctl daemon-reload
  systemctl enable --now proxy-manager-recover.timer
  systemctl enable --now proxy-manager-auth
  curl --retry 10 --retry-connrefused --retry-delay 1 -fsS --max-time 3 http://127.0.0.1:18081/health >/dev/null
  systemctl is-active --quiet proxy-manager-auth
  touch "$STATE_DIR/live-users"
  if ! (write_xray && write_hysteria && systemctl restart xray hysteria-server &&
        systemctl is-active --quiet xray && systemctl is-active --quiet hysteria-server); then
    if (( ! was_live )); then rm -f "$STATE_DIR/live-users"; fi
    cp -a "$backup/xray-config.json" "$XRAY_CONF"
    cp -a "$backup/hysteria-config.yaml" "$HY_CONF"
    systemctl restart xray hysteria-server || true
    die "Migration failed; previous daemon configs restored. Backup: $backup"
  fi
  log "Live account updates enabled. Backup: $backup"
}

recover_live_users() {
  [[ -s $STATE_DIR/pending-user ]] || return 0
  local pending
  pending=$(cat "$STATE_DIR/pending-user")
  valid_account_name "$pending" || return 1
  live_users_ready || return 1
  apply_live_account "$pending" || return 1
  rm -f "$STATE_DIR/pending-user"
}

transactional_account_change() {
  local op=$1 name=$2 previous backup_dir
  live_users_ready || die "Live provisioning is not configured or auth is unavailable. Run upgrade-live-users.sh from the updated bundle first."
  recover_live_users || die "Previous account change still needs recovery; retry after restoring daemon health."
  previous="$WORK/state-before.json"
  cp -a "$STATE_FILE" "$previous"
  backup_dir=$(backup_manager_state)

  valid_account_name "$name" || die "Invalid account name."
  if [[ $op == add ]]; then
    ! account_exists "$name" || die "Account already exists."
  else
    account_exists "$name" || die "Account not found."
  fi
  printf '%s\n' "$name" >"$STATE_DIR/pending-user.tmp"
  chmod 600 "$STATE_DIR/pending-user.tmp"
  mv "$STATE_DIR/pending-user.tmp" "$STATE_DIR/pending-user"
  case "$op" in
    add) account_add_state "$name" ;;
    enable) account_set_enabled_state "$name" true ;;
    disable) account_set_enabled_state "$name" false ;;
    rotate) account_rotate_state "$name" ;;
    delete) account_delete_state "$name" ;;
    *) die "Unknown account operation: $op" ;;
  esac

  if ! (apply_live_account "$name"); then
    warn "Applying the new account state failed. Rolling back."
    cp -a "$previous" "$STATE_FILE.tmp"
    mv -f -- "$STATE_FILE.tmp" "$STATE_FILE"
    if ! (apply_live_account "$name"); then
      die "Rollback also failed. Backup: $backup_dir"
    fi
    rm -f "$STATE_DIR/pending-user"
    die "Account change was rolled back. Backup: $backup_dir"
  fi
  rm -f "$STATE_DIR/pending-user"
  log "Account operation '$op' applied. Backup: $backup_dir"
}

current_xray_version() {
  command -v xray >/dev/null 2>&1 || return 1
  xray version 2>/dev/null | awk 'NR==1 {print "v" $2}'
}

pin_xray_version() {
  local version=$1
  validate_xray_version "$version"
  ensure_state_dir
  printf '%s\n' "$version" >"$XRAY_VERSION_PIN"
  chmod 600 "$XRAY_VERSION_PIN"
  chown root:root "$XRAY_VERSION_PIN"
}

xray_test_version() {
  local candidate=$1 previous
  validate_xray_version "$candidate"
  previous=$(current_xray_version || true)
  [[ -n $previous ]] || die "Could not determine currently installed Xray version."

  log "Testing Xray compatibility candidate: $candidate (current: $previous)"
  systemctl stop xray 2>/dev/null || true
  if ! install_xray_release "$candidate"; then
    warn "Could not install candidate $candidate. Restoring $previous."
    install_xray_release "$previous" >/dev/null 2>&1 || die "Failed to restore Xray $previous."
    systemctl restart xray
    return 1
  fi

  # Validate the existing server config before starting the service.
  if ! xray run -test -config "$XRAY_CONF" >/dev/null 2>&1; then
    warn "Xray $candidate rejected the existing server config. Restoring $previous."
    install_xray_release "$previous" >/dev/null 2>&1 || die "Failed to restore Xray $previous."
    systemctl restart xray
    return 1
  fi

  systemctl restart xray
  sleep 1
  if ! systemctl is-active --quiet xray; then
    warn "Xray $candidate service did not start. Restoring $previous."
    install_xray_release "$previous" >/dev/null 2>&1 || die "Failed to restore Xray $previous."
    systemctl restart xray
    return 1
  fi

  load_state
  if self_test_xray_e2e; then
    pin_xray_version "$candidate"
    log "Xray $candidate PASSED local REALITY E2E and is now pinned."
    return 0
  fi

  warn "Xray $candidate failed local REALITY E2E. Restoring $previous."
  systemctl stop xray 2>/dev/null || true
  install_xray_release "$previous" >/dev/null 2>&1 || die "Failed to restore Xray $previous."
  systemctl restart xray
  return 1
}

xray_matrix() {
  local -a candidates=("$@")
  if (( ${#candidates[@]} == 0 )); then
    # All are official releases and below the 26.9.8 REALITY compatibility break.
    candidates=(v26.6.27 v26.6.22 v26.6.1 v26.5.9 v26.5.3 v26.4.25 v26.3.27)
  fi

  local v
  echo "Xray compatibility matrix (state/credentials are not modified):"
  for v in "${candidates[@]}"; do
    echo
    echo "=== $v ==="
    if xray_test_version "$v"; then
      echo
      echo "Selected Xray version: $v"
      echo "Pinned at: $XRAY_VERSION_PIN"
      echo "Next: test the same subscription from sing-box 1.14.4."
      return 0
    fi
  done
  die "No Xray candidate passed local REALITY E2E. The original version was restored after each failed candidate."
}

manager_usage() {
  show_help
}

manager_main() {
  local cmd=${1:-} sub=${2:-} name=${3:-}
  load_state

  case "$cmd" in
    account)
      case "$sub" in
        list)
          printf '%-24s %-10s %-38s\n' NAME STATUS XRAY_UUID
          jq -r '.accounts[] | [.name, (if .enabled then "enabled" else "disabled" end), .xray_uuid] | @tsv' "$STATE_FILE" \
            | while IFS=$'\t' read -r n st u; do printf '%-24s %-10s %-38s\n' "$n" "$st" "$u"; done
          ;;
        show)
          [[ -n $name ]] || die "Account name is required."
          account_links "$name"
          ;;
        add|enable|disable|rotate|delete)
          [[ -n $name ]] || die "Account name is required."
          transactional_account_change "$sub" "$name"
          load_state
          [[ $sub == delete ]] || account_links "$name"
          ;;
        *) manager_usage; exit 2 ;;
      esac
      ;;
    xray)
      case "$sub" in
        version)
          printf 'Installed: %s\n' "$(current_xray_version || echo unknown)"
          if [[ -s $XRAY_VERSION_PIN ]]; then
            printf 'Pinned:    %s\n' "$(cat "$XRAY_VERSION_PIN")"
          else
            echo 'Pinned:    (none)'
          fi
          ;;
        test)
          [[ -n $name ]] || die "Xray version is required. Example: proxy-manager xray test v26.7.28"
          xray_test_version "$name"
          ;;
        matrix)
          shift 2
          xray_matrix "$@"
          ;;
        *) manager_usage; exit 2 ;;
      esac
      ;;
    recover-live-users)
      recover_live_users
      ;;
    enable-live-users)
      enable_live_users
      ;;
    apply)
      apply_account_state
      log "Configuration regenerated and services restarted."
      ;;
    status)
      systemctl --no-pager --full status xray hysteria-server nginx || true
      echo
      ss -H -lntup "sport = :$PORT" 2>/dev/null || true
      ;;
    self-test)
      if self_test; then
        exit 0
      else
        exit 1
      fi
      ;;
    *) manager_usage; exit 2 ;;
  esac
}

install_manager_binary() {
  local src
  src=$(readlink -f "$0")
  if [[ $src != "$MANAGER_BIN" ]]; then
    install -m 755 "$src" "$MANAGER_BIN"
  fi
}

# ---------- main ----------
[[ $EUID -eq 0 ]] || die "Run this script as root (sudo -i)."
# An idle recovery timer must not create thousands of empty log files.
if [[ ${1:-} == recover-live-users && ! -s $STATE_DIR/pending-user ]]; then exit 0; fi
setup_run_log
# One stable lock covers install, migration, recovery, CLI and Telegram mutations.
ensure_state_dir
exec 9>"$STATE_DIR/accounts.lock"
flock -x 9

# Management commands do not participate in install flag parsing.
if [[ ${1:-} == recover-live-users || ${1:-} == enable-live-users || ${1:-} == account || ${1:-} == apply || ${1:-} == status || ${1:-} == self-test || ${1:-} == xray ]]; then
  manager_main "$@"
  exit 0
fi

PRESERVE_SECRETS=0
INSTALL_REQUESTED=0
while (($#)); do
  case "$1" in
    install)
      (( INSTALL_REQUESTED == 0 )) || die "'install' was specified more than once."
      INSTALL_REQUESTED=1
      ;;
    -S|--preserve-secrets)
      PRESERVE_SECRETS=1
      ;;
    -h|--help|help)
      show_help
      exit 0
      ;;
    *)
      echo "Unknown option or command: $1" >&2
      echo >&2
      show_help >&2
      exit 2
      ;;
  esac
  shift
done

install_deps
DETECTED_PUBLIC_IP=$(detect_ip)
EXISTING_STATE=0
if [[ -s $STATE_FILE ]]; then
  EXISTING_STATE=1
  load_state
  # Explicit environment values win over persisted state on reinstall.
  [[ -n $REQUESTED_XRAY_SNI ]] && XRAY_SNI="$REQUESTED_XRAY_SNI"
  [[ -n $REQUESTED_HY_SNI ]] && HY_SNI="$REQUESTED_HY_SNI"
  [[ $REQUESTED_HY_INSECURE_SET == 1 ]] && HY_INSECURE="$REQUESTED_HY_INSECURE"
  PUBLIC_IP="$DETECTED_PUBLIC_IP"
  if (( PRESERVE_SECRETS )); then
    log "Existing manager state found; -S enabled, all VPN credentials will be preserved."
  else
    log "Existing manager state found; account list/state will be preserved, but all VPN credentials will be rotated."
  fi
else
  PUBLIC_IP="$DETECTED_PUBLIC_IP"
  if (( PRESERVE_SECRETS )); then
    warn "-S was requested, but no existing state exists. A fresh installation will create new credentials."
  fi
fi

log "Public IP: $PUBLIC_IP"
collect_input
install_xray
install_hysteria
stop_services
check_ports
backup_existing
create_admin_user
setup_ssh_key_auth
setup_firewall
install_web
prepare_site
write_nginx_http
write_cert_hook
obtain_cert
write_nginx_tls

if (( EXISTING_STATE )); then
  update_server_state
  if (( ! PRESERVE_SECRETS )); then
    rotate_all_credentials
  fi
  load_state
else
  gen_server_secrets
  create_initial_state
  load_state
fi

if [[ -f $STATE_DIR/live-users ]]; then write_auth_snapshot; fi
write_xray
write_hysteria
start_services
tune_net
self_test || true
install_manager_binary
print_links
