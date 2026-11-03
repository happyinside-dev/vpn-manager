#!/usr/bin/env bash
# proxy-setup.sh - Xray VLESS/REALITY + Hysteria 2 installer.
# Debian/Ubuntu, systemd required. Run as root.
#
# Examples:
#   bash proxy-setup.sh
#   bash proxy-setup.sh -sni "example.com"
#   bash proxy-setup.sh -xsni "www.microsoft.com" -hsni "www.apple.com"
#   bash proxy-setup.sh -s
#   bash proxy-setup.sh -s -sni "example.com"
#
# Options:
#   -sni DOMAIN, --sni DOMAIN   Set both Xray and Hysteria SNI.
#   -xsni DOMAIN, --xsni DOMAIN Override Xray SNI only.
#   -hsni DOMAIN, --hsni DOMAIN Override Hysteria SNI only.
#   -s, --keep-secrets          Preserve server-level REALITY/TLS secrets.
#   -h, --help                  Show help.
#
# Environment variables:
#   PORT=443
#   SKIP_FIREWALL=1
#   SSH_HARDEN=0
#   SSH_PUBKEY="ssh-ed25519 ..."
#   SNI=example.com
#   XSNI=www.microsoft.com
#   HSNI=www.apple.com
#   PROXY_MANAGER_SOURCE=/path/to/proxy-manager
#   PROXY_MANAGER_BIN=/path/to/prebuilt/proxy-manager
#   PM_ADMIN_USERNAME=admin
#   PM_ADMIN_PASSWORD=...   # optional; otherwise prompted on first install
#
# Logs:
#   /var/log/proxy-setup/proxy-setup-YYYYMMDD-HHMMSS.log
#   /var/log/proxy-setup/latest.log
#
# Backups:
#   /var/backups/proxy-setup/YYYYMMDD-HHMMSS/
#
# Client links:
#   /home/proxy/proxy-links.txt

set -Eeuo pipefail

PORT="${PORT:-443}"
SKIP_FIREWALL="${SKIP_FIREWALL:-0}"
SSH_HARDEN="${SSH_HARDEN:-1}"
SSH_PUBKEY="${SSH_PUBKEY:-}"
SNI="${SNI:-}"
XSNI="${XSNI:-}"
HSNI="${HSNI:-}"
SNI_EXPLICIT=0
XSNI_EXPLICIT=0
HSNI_EXPLICIT=0
KEEP_SECRETS=0
[[ -n "$SNI" ]] && SNI_EXPLICIT=1
[[ -n "$XSNI" ]] && XSNI_EXPLICIT=1
[[ -n "$HSNI" ]] && HSNI_EXPLICIT=1

NEW_USER="proxy"
DONOR_PORT=443
XRAY_CONF="/usr/local/etc/xray/config.json"
HY_DIR="/etc/hysteria"
HY_CONF="$HY_DIR/config.yaml"
BACKUP_ROOT="/var/backups/proxy-setup"
LOG_ROOT="/var/log/proxy-setup"

PM_USER="proxy-manager"
PM_GROUP="proxy-manager"
PM_ETC="/etc/proxy-manager"
PM_STATE="/var/lib/proxy-manager"
PM_LOG="/var/log/proxy-manager"
PM_DB="$PM_STATE/proxy-manager.db"
PM_BIN="/usr/local/bin/proxy-manager"
PM_CONFIG="$PM_ETC/config.yaml"
PM_ADMIN_USERNAME="${PM_ADMIN_USERNAME:-admin}"
PM_ADMIN_PASSWORD="${PM_ADMIN_PASSWORD:-}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_PM_SOURCE="$(cd -- "$SCRIPT_DIR/.." 2>/dev/null && pwd || true)"
PROXY_MANAGER_SOURCE="${PROXY_MANAGER_SOURCE:-$DEFAULT_PM_SOURCE}"
PROXY_MANAGER_BIN="${PROXY_MANAGER_BIN:-}"
LEGACY_PRESENT=0
INITIAL_ACCOUNT=""
XRAY_SNI=""
HY_SNI=""
OLD_XRAY_SNI=""
OLD_HY_SNI=""

FALLBACK_SNI=(
  www.microsoft.com
  dl.google.com
  www.apple.com
  addons.mozilla.org
)

usage() {
  cat <<'USAGE'
Usage:
  proxy-setup.sh [options]

Options:
  -sni DOMAIN, --sni DOMAIN
      Set both Xray REALITY SNI/target and Hysteria SNI/masquerade.

  -xsni DOMAIN, --xsni DOMAIN
      Override Xray REALITY SNI/target only.

  -hsni DOMAIN, --hsni DOMAIN
      Override Hysteria SNI/masquerade only.

  -s, --keep-secrets
      Preserve server-level secrets on rerun: REALITY private/public key,
      short ID and Hysteria certificate/key. Existing Proxy Manager users and
      its SQLite database are always preserved.

  -h, --help
      Show this help.

Environment variables:
  PORT=443
  SKIP_FIREWALL=1
  SSH_HARDEN=0
  SSH_PUBKEY="ssh-ed25519 ..."
  SNI=example.com
  XSNI=www.microsoft.com
  HSNI=www.apple.com
  PROXY_MANAGER_SOURCE=/path/to/proxy-manager
  PROXY_MANAGER_BIN=/path/to/prebuilt/proxy-manager
  PM_ADMIN_USERNAME=admin
  PM_ADMIN_PASSWORD=...  # optional; prompted on first Proxy Manager install
USAGE
}
while (( $# > 0 )); do
  case "$1" in
    -sni|--sni)
      [[ $# -ge 2 ]] || { echo "Error: $1 requires a domain." >&2; exit 2; }
      SNI="$2"; SNI_EXPLICIT=1; shift 2 ;;
    --sni=*) SNI="${1#*=}"; SNI_EXPLICIT=1; shift ;;
    -xsni|--xsni)
      [[ $# -ge 2 ]] || { echo "Error: $1 requires a domain." >&2; exit 2; }
      XSNI="$2"; XSNI_EXPLICIT=1; shift 2 ;;
    --xsni=*) XSNI="${1#*=}"; XSNI_EXPLICIT=1; shift ;;
    -hsni|--hsni)
      [[ $# -ge 2 ]] || { echo "Error: $1 requires a domain." >&2; exit 2; }
      HSNI="$2"; HSNI_EXPLICIT=1; shift 2 ;;
    --hsni=*) HSNI="${1#*=}"; HSNI_EXPLICIT=1; shift ;;
    -s|--keep-secrets) KEEP_SECRETS=1; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    *) echo "Unknown option: $1" >&2; echo "Run with --help for usage." >&2; exit 2 ;;
  esac
done

c_g=$'\e[32m'
c_y=$'\e[33m'
c_r=$'\e[31m'
c_b=$'\e[34m'
c_0=$'\e[0m'

mkdir -p "$LOG_ROOT"
chmod 700 "$LOG_ROOT"

RUN_TS=$(date +%Y%m%d-%H%M%S)
LOG_FILE="$LOG_ROOT/proxy-setup-$RUN_TS.log"

touch "$LOG_FILE"
chmod 600 "$LOG_FILE"

ln -sfn "$LOG_FILE" "$LOG_ROOT/latest.log"

WORK=$(mktemp -d)

log_raw() {
  printf '%s\n' "$*" >>"$LOG_FILE"
}

log() {
  local m="$*"
  echo "${c_g}[+]${c_0} $m" >&2
  log_raw "[$(date -Is)] [INFO] $m"
}

warn() {
  local m="$*"
  echo "${c_y}[!]${c_0} $m" >&2
  log_raw "[$(date -Is)] [WARN] $m"
}

die() {
  local m="$*"
  echo "${c_r}[x]${c_0} $m" >&2
  log_raw "[$(date -Is)] [ERROR] $m"
  exit 1
}

section() {
  local m="$*"
  echo "${c_b}[*]${c_0} $m" >&2
  log_raw ""
  log_raw "[$(date -Is)] [SECTION] $m"
}

dump_diagnostics() {
  {
    echo
    echo "===== FAILURE DIAGNOSTICS ====="
    echo "Timestamp: $(date -Is)"

    echo
    echo "--- uname ---"
    uname -a 2>&1 || true

    echo
    echo "--- OS ---"
    cat /etc/os-release 2>&1 || true

    echo
    echo "--- IP addresses ---"
    ip addr 2>&1 || true

    echo
    echo "--- routes ---"
    ip route 2>&1 || true

    echo
    echo "--- listening ports ---"
    ss -lntup 2>&1 || true

    echo
    echo "--- Xray status ---"
    systemctl status xray --no-pager -l 2>&1 || true

    echo
    echo "--- Xray journal ---"
    journalctl -u xray -n 100 --no-pager 2>&1 || true

    echo
    echo "--- Hysteria status ---"
    systemctl status hysteria-server --no-pager -l 2>&1 || true

    echo
    echo "--- Hysteria journal ---"
    journalctl -u hysteria-server -n 100 --no-pager 2>&1 || true

    echo
    echo "--- SSH status ---"
    systemctl status ssh --no-pager -l 2>&1 \
      || systemctl status sshd --no-pager -l 2>&1 \
      || true

    echo
    echo "--- UFW status ---"
    ufw status verbose 2>&1 || true

    echo "===== END FAILURE DIAGNOSTICS ====="
  } >>"$LOG_FILE"
}

on_error() {
  local rc=$?
  local line="${BASH_LINENO[0]:-unknown}"
  local cmd="${BASH_COMMAND:-unknown}"

  set +e

  log_raw ""
  log_raw "[$(date -Is)] [FATAL] Script failed."
  log_raw "[$(date -Is)] [FATAL] Exit code: $rc"
  log_raw "[$(date -Is)] [FATAL] Line: $line"
  log_raw "[$(date -Is)] [FATAL] Command: $cmd"

  dump_diagnostics

  echo "${c_r}[x]${c_0} Script failed." >&2
  echo "Log file: $LOG_FILE" >&2

  exit "$rc"
}

on_exit() {
  local rc=$?

  rm -rf "$WORK"

  if (( rc == 0 )); then
    log_raw "[$(date -Is)] [SUCCESS] Script completed successfully."
  fi
}

trap on_error ERR
trap on_exit EXIT

log "Log file: $LOG_FILE"

[[ $EUID -eq 0 ]] || die "Run this script as root."

[[ -r /etc/os-release ]] && . /etc/os-release

if [[ "${ID:-}" != "debian" && "${ID:-}" != "ubuntu" ]]; then
  warn "This script targets Debian/Ubuntu. Detected: ${ID:-unknown}."
fi

command -v systemctl >/dev/null || die "systemd is required."

[[ "$PORT" =~ ^[0-9]+$ ]] || die "PORT must be an integer."

(( PORT >= 1 && PORT <= 65535 )) \
  || die "PORT must be between 1 and 65535."

validate_hostname() {
  local label="$1" value="$2"
  [[ "$value" =~ ^[A-Za-z0-9.-]+$ && "$value" == *.* ]] \
    || die "Invalid $label hostname: $value"
}

(( SNI_EXPLICIT == 0 )) || validate_hostname "SNI" "$SNI"
(( XSNI_EXPLICIT == 0 )) || validate_hostname "Xray SNI" "$XSNI"
(( HSNI_EXPLICIT == 0 )) || validate_hostname "Hysteria SNI" "$HSNI"

install_deps() {
  section "Installing dependencies"

  export DEBIAN_FRONTEND=noninteractive

  apt-get update 2>&1 \
    | tee -a "$LOG_FILE"

  apt-get install -y \
    curl \
    jq \
    openssl \
    ca-certificates \
    ufw \
    iproute2 \
    coreutils \
    openssh-client \
    openssh-server \
    sudo \
    sqlite3 \
    2>&1 | tee -a "$LOG_FILE"

  log "Dependencies installed."
}

detect_ip() {
  local ip u

  for u in \
    https://api.ipify.org \
    https://ifconfig.me \
    https://icanhazip.com
  do
    log_raw "[$(date -Is)] [DEBUG] Trying public IP provider: $u"

    ip=$(
      curl -4 -fsS --max-time 8 "$u" 2>>"$LOG_FILE" \
        | tr -d '[:space:]'
    ) || continue

    if [[ $ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      printf '%s\n' "$ip"
      return 0
    fi
  done

  return 1
}

ip2int() {
  local a b c d

  IFS=. read -r a b c d <<<"$1"

  echo $(( (a << 24) | (b << 16) | (c << 8) | d ))
}

int2ip() {
  local n="$1"

  echo "$(( (n >> 24) & 255 )).$(( (n >> 16) & 255 )).$(( (n >> 8) & 255 )).$(( n & 255 ))"
}

cidr_of() {
  local ip="$1"
  local prefix="$2"
  local n
  local mask

  n=$(ip2int "$ip")

  if (( prefix == 0 )); then
    mask=0
  else
    mask=$(( (0xFFFFFFFF << (32 - prefix)) & 0xFFFFFFFF ))
  fi

  echo "$(int2ip $(( n & mask )))/$prefix"
}

install_xray() {
  section "Installing Xray"

  local installer="$WORK/xray-install.sh"

  curl -fsSL \
    https://github.com/XTLS/Xray-install/raw/main/install-release.sh \
    -o "$installer" \
    2>>"$LOG_FILE"

  chmod 700 "$installer"

  bash "$installer" install \
    > >(tee -a "$LOG_FILE") \
    2> >(tee -a "$LOG_FILE" >&2)

  command -v xray >/dev/null \
    || die "xray was not found after installation."

  log "Xray installed: $(xray version 2>/dev/null | head -n1 || echo unknown-version)"
}

install_hysteria() {
  section "Installing Hysteria 2"

  local installer="$WORK/hysteria-install.sh"
  local old_term
  local hv

  curl -fsSL \
    https://get.hy2.sh/ \
    -o "$installer" \
    2>>"$LOG_FILE"

  chmod 700 "$installer"

  old_term="${TERM:-}"
  export TERM=xterm-256color

  bash "$installer" \
    > >(tee -a "$LOG_FILE") \
    2> >(tee -a "$LOG_FILE" >&2)

  if [[ -n "$old_term" ]]; then
    export TERM="$old_term"
  else
    unset TERM
  fi

  command -v hysteria >/dev/null \
    || die "hysteria was not found after installation."

  hv=$(hysteria version 2>&1 | head -n1 || true)

  [[ -n "$hv" ]] || hv="installed"

  log "Hysteria installed: $hv"
}

stop_services() {
  section "Stopping VPN/control services"
  systemctl stop proxy-manager 2>>"$LOG_FILE" || true
  systemctl stop xray 2>>"$LOG_FILE" || true
  systemctl stop hysteria-server 2>>"$LOG_FILE" || true
  log "Services stopped."
}

check_ports() {
  section "Checking port $PORT"

  local busy

  busy=$(
    ss -H -lntup "sport = :$PORT" 2>>"$LOG_FILE" || true
  )

  if [[ -n "$busy" ]]; then
    log_raw "$busy"
    die "Port $PORT is already in use."
  fi

  log "Port $PORT is available."
}

check_tls() {
  local address="$1"
  local domain="$2"
  local out

  log_raw "[$(date -Is)] [DEBUG] TLS check: address=$address domain=$domain"

  out=$(
    echo \
      | timeout 10 \
        openssl s_client \
          -connect "$address:$DONOR_PORT" \
          -servername "$domain" \
          -tls1_3 \
          -alpn h2 \
          -verify_return_error \
          2>&1
  ) || {
    log_raw "[$(date -Is)] [DEBUG] TLS check failed for $address / $domain"
    return 1
  }

  grep -q 'TLSv1.3' <<<"$out" \
    && grep -q 'ALPN protocol: h2' <<<"$out"
}

find_neighbor_sni() {
  local pat
  local url
  local bin
  local step
  local prefix
  local limit
  local cidr
  local csv
  local ip
  local dom
  local tries
  local rel

  case "$(uname -m)" in
    x86_64|amd64)
      pat='linux-(64|amd64)$'
      ;;
    aarch64|arm64)
      pat='linux-arm64$'
      ;;
    *)
      warn "This architecture is not supported by RealiTLScanner."
      return 1
      ;;
  esac

  section "Downloading RealiTLScanner"

  rel=$(
    curl -fsSL \
      --max-time 20 \
      https://api.github.com/repos/XTLS/RealiTLScanner/releases/latest \
      2>>"$LOG_FILE"
  ) || {
    warn "GitHub API request failed."
    return 1
  }

  url=$(
    jq -r \
      --arg p "$pat" \
      '[.assets[]? | select(.name|test($p))][0].browser_download_url // empty' \
      <<<"$rel"
  ) || url=""

  [[ -n "$url" ]] || {
    warn "No matching RealiTLScanner binary was found."
    return 1
  }

  bin="$WORK/scanner"

  curl -fsSL \
    --max-time 120 \
    -o "$bin" \
    "$url" \
    2>>"$LOG_FILE" || {
      warn "Could not download RealiTLScanner."
      return 1
    }

  chmod +x "$bin"

  log "RealiTLScanner downloaded: ${url##*/}"

  for step in \
    "24 60" \
    "22 120" \
    "20 300"
  do
    read -r prefix limit <<<"$step"

    cidr=$(cidr_of "$PUBLIC_IP" "$prefix")
    csv="$WORK/scan_$prefix.csv"

    rm -f "$csv"

    section "Scanning nearby addresses: $cidr"

    log_raw "[$(date -Is)] [DEBUG] Scanner timeout: ${limit}s"
    log_raw "[$(date -Is)] [DEBUG] Scanner threads: 100"

    timeout "$limit" \
      "$bin" \
      -addr "$cidr" \
      -port "$DONOR_PORT" \
      -thread 100 \
      -timeout 5 \
      -out "$csv" \
      >>"$LOG_FILE" 2>&1 || true

    if [[ ! -s "$csv" ]]; then
      warn "Scanner returned no results for $cidr."
      continue
    fi

    log "Scanner output file created: $csv"

    tries=0

    while IFS=, read -r ip dom; do
      ip=${ip//\"/}
      ip=${ip%$'\r'}

      dom=${dom//\"/}
      dom=${dom%$'\r'}

      log_raw "[$(date -Is)] [DEBUG] CSV candidate: ip=$ip domain=$dom"

      [[ -n "$dom" ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: empty domain"
        continue
      }

      [[ "$dom" != \** ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: wildcard domain=$dom"
        continue
      }

      [[ "$dom" == *.* ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: invalid hostname=$dom"
        continue
      }

      [[ "$dom" != *' '* ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: hostname contains spaces=$dom"
        continue
      }

      [[ ! "$dom" =~ ^[0-9.]+$ ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: hostname is an IP=$dom"
        continue
      }

      [[ "$ip" != "$PUBLIC_IP" ]] || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: candidate is local VPS IP"
        continue
      }

      getent ahosts "$dom" >/dev/null 2>&1 || {
        log_raw "[$(date -Is)] [DEBUG] Reject candidate: DNS resolution failed for $dom"
        continue
      }

      tries=$((tries + 1))

      if (( tries > 25 )); then
        log_raw "[$(date -Is)] [DEBUG] Candidate limit reached"
        break
      fi

      log_raw "[$(date -Is)] [DEBUG] Testing SNI candidate: domain=$dom address=$ip"

      if check_tls "$ip" "$dom"; then
        SNI="$dom"
        log "Reality target found: $SNI (address $ip)"
        return 0
      fi

      log_raw "[$(date -Is)] [DEBUG] Reject candidate: TLS validation failed for $dom at $ip"

    done < <(
      awk -F, '
        NR > 1 {
          gsub(/\r/, "", $1)
          gsub(/\r/, "", $3)
          print $1 "," $3
        }
      ' "$csv" \
        | sort -u -t, -k2,2 \
        | shuf
    )
  done

  return 1
}

pick_sni() {
  section "Selecting Reality SNI"

  if [[ -n "$SNI" ]]; then
    log "SNI provided: $SNI"

    if check_tls "$SNI" "$SNI"; then
      log "SNI validation successful: $SNI"
      return 0
    fi

    if (( SNI_EXPLICIT == 1 )); then
      die "Explicitly supplied SNI failed TLS 1.3 + HTTP/2 validation: $SNI"
    fi

    warn "Existing SNI did not pass validation, but it is being preserved because --keep-secrets is active."

    return 0
  fi

  if find_neighbor_sni; then
    return 0
  fi

  warn "No nearby SNI was found. Trying fallback domains."

  local d

  for d in "${FALLBACK_SNI[@]}"; do
    log_raw "[$(date -Is)] [DEBUG] Testing fallback SNI: $d"

    if check_tls "$d" "$d"; then
      SNI="$d"
      log "Using fallback SNI: $SNI"
      return 0
    fi
  done

  die "Could not select a valid SNI."
}

backup_existing() {
  section "Backing up existing configuration"
  local ts backup_dir found=0 admin_home auth_file f
  ts=$(date +%Y%m%d-%H%M%S)
  backup_dir="$BACKUP_ROOT/$ts"
  admin_home=$(getent passwd "$NEW_USER" 2>/dev/null | cut -d: -f6 || true)
  auth_file="${admin_home:+$admin_home/.ssh/authorized_keys}"
  local files=(
    "$XRAY_CONF" "$HY_CONF" "$HY_DIR/server.key" "$HY_DIR/server.crt"
    "$PM_CONFIG" "$PM_DB" "$PM_DB-wal" "$PM_DB-shm"
    "/etc/systemd/system/proxy-manager.service"
    "/root/proxy-links.txt" "/home/$NEW_USER/proxy-links.txt"
    "/etc/ssh/sshd_config"
    "/etc/ssh/sshd_config.d/00-proxy-setup-hardening.conf"
    "/etc/ssh/sshd_config.d/99-proxy-setup-hardening.conf"
  )
  [[ -n "$auth_file" ]] && files+=("$auth_file")
  for f in "${files[@]}"; do
    [[ -e "$f" ]] || continue
    if (( found == 0 )); then
      mkdir -p "$backup_dir"; chmod 700 "$BACKUP_ROOT" "$backup_dir"; found=1
    fi
    mkdir -p "$backup_dir$(dirname "$f")"
    cp -a -- "$f" "$backup_dir$f"
    log_raw "[$(date -Is)] [BACKUP] $f -> $backup_dir$f"
  done
  if (( found == 1 )); then log "Previous configuration backed up to: $backup_dir"; else log "No existing configuration found."; fi
}

prompt_user_password() {
  local pass1
  local pass2

  [[ -r /dev/tty && -w /dev/tty ]] \
    || die "Interactive terminal is required to set the password for $NEW_USER."

  echo >/dev/tty
  echo "Configure password for admin user '$NEW_USER'." >/dev/tty

  while true; do
    printf 'Password for %s: ' "$NEW_USER" >/dev/tty

    IFS= read -r -s pass1 </dev/tty \
      || die "Could not read password."

    printf '\n' >/dev/tty

    printf 'Repeat password: ' >/dev/tty

    IFS= read -r -s pass2 </dev/tty \
      || die "Could not read password."

    printf '\n' >/dev/tty

    [[ -n "$pass1" ]] || {
      warn "Password must not be empty."
      continue
    }

    [[ "$pass1" == "$pass2" ]] || {
      warn "Passwords do not match."
      continue
    }

    printf '%s' "$pass1"

    return 0
  done
}

create_admin_user() {
  section "Configuring admin user '$NEW_USER'"

  getent group sudo >/dev/null \
    || die "The sudo group does not exist."

  local password
  local home

  password=$(prompt_user_password)

  if id "$NEW_USER" >/dev/null 2>&1; then
    usermod -aG sudo "$NEW_USER"

    printf '%s:%s\n' "$NEW_USER" "$password" \
      | chpasswd

    log "User $NEW_USER already exists. Password updated and sudo rights ensured."
  else
    useradd \
      -m \
      -s /bin/bash \
      -G sudo \
      "$NEW_USER"

    printf '%s:%s\n' "$NEW_USER" "$password" \
      | chpasswd

    log "User $NEW_USER created and added to sudo group."
  fi

  password=""

  home=$(
    getent passwd "$NEW_USER" \
      | cut -d: -f6
  )

  [[ -n "$home" && -d "$home" ]] \
    || die "Home directory for $NEW_USER is invalid."

  log_raw "[$(date -Is)] [INFO] User home: $home"
  log_raw "[$(date -Is)] [INFO] User groups: $(id "$NEW_USER")"
}

setup_ssh_key_auth() {
  section "Configuring SSH key authentication"

  if [[ "$SSH_HARDEN" == 0 ]]; then
    warn "SSH hardening skipped because SSH_HARDEN=0."
    return 0
  fi

  id "$NEW_USER" >/dev/null 2>&1 \
    || die "User $NEW_USER does not exist."

  command -v sshd >/dev/null \
    || die "sshd is required."

  command -v ssh-keygen >/dev/null \
    || die "ssh-keygen is required."

  local key="$SSH_PUBKEY"
  local key_file
  local home
  local group
  local auth_dir
  local auth_file
  local dropin
  local fingerprint
  local effective

  home=$(
    getent passwd "$NEW_USER" \
      | cut -d: -f6
  )

  group=$(
    id -gn "$NEW_USER"
  )

  [[ -n "$home" && -d "$home" ]] \
    || die "Could not determine home directory for $NEW_USER."

  if [[ -z "$key" ]]; then
    if [[ -r /dev/tty && -w /dev/tty ]]; then
      echo >/dev/tty

      echo "SSH password authentication will be disabled after validation." >/dev/tty
      echo "Paste ONE SSH public key for user '$NEW_USER'." >/dev/tty

      printf 'Public key: ' >/dev/tty

      IFS= read -r key </dev/tty \
        || die "Could not read the SSH public key."
    else
      warn "No interactive terminal and SSH_PUBKEY is empty. SSH hardening skipped."
      return 0
    fi
  fi

  [[ "$key" != *$'\n'* && "$key" != *$'\r'* ]] \
    || die "SSH public key must be a single line."

  [[ "$key" =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521))[[:space:]]+[^[:space:]]+([[:space:]].*)?$ ]] \
    || die "Unsupported or malformed SSH public key."

  key_file="$WORK/ssh-public-key.pub"

  printf '%s\n' "$key" >"$key_file"

  ssh-keygen -l -f "$key_file" >/dev/null 2>&1 \
    || die "ssh-keygen rejected the supplied public key."

  fingerprint=$(
    ssh-keygen -lf "$key_file" \
      | awk '{print $2, $3}'
  )

  log "Validated SSH key fingerprint: $fingerprint"

  auth_dir="$home/.ssh"
  auth_file="$auth_dir/authorized_keys"

  install \
    -d \
    -m 700 \
    -o "$NEW_USER" \
    -g "$group" \
    "$auth_dir"

  touch "$auth_file"

  chown "$NEW_USER:$group" "$auth_file"
  chmod 600 "$auth_file"

  if ! grep -qxF -- "$key" "$auth_file"; then
    printf '%s\n' "$key" >>"$auth_file"

    log "SSH public key added to $auth_file."
  else
    log "SSH public key is already present."
  fi

  dropin="/etc/ssh/sshd_config.d/00-proxy-setup-hardening.conf"

  mkdir -p /etc/ssh/sshd_config.d

  cat >"$dropin" <<'SSHD'
# Managed by proxy-setup.sh
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
SSHD

  chmod 644 "$dropin"

  if ! sshd -t >>"$LOG_FILE" 2>&1; then
    rm -f "$dropin"

    die "sshd rejected the hardening configuration."
  fi

  effective=$(
    sshd -T 2>>"$LOG_FILE"
  ) || {
    rm -f "$dropin"

    die "Could not read effective sshd configuration."
  }

  grep -qx 'pubkeyauthentication yes' <<<"$effective" || {
    rm -f "$dropin"

    die "Effective SSH configuration does not enable public key authentication."
  }

  grep -qx 'passwordauthentication no' <<<"$effective" || {
    rm -f "$dropin"

    die "Effective SSH configuration still allows password authentication."
  }

  grep -qx 'kbdinteractiveauthentication no' <<<"$effective" || {
    rm -f "$dropin"

    die "Effective SSH configuration still allows keyboard-interactive authentication."
  }

  if systemctl reload ssh >>"$LOG_FILE" 2>&1 \
      || systemctl reload sshd >>"$LOG_FILE" 2>&1
  then
    rm -f /etc/ssh/sshd_config.d/99-proxy-setup-hardening.conf

    log "SSH key authentication configured for $NEW_USER."
    log "SSH password authentication disabled."
    log "Root SSH access remains available by public key only."
  else
    rm -f "$dropin"

    systemctl reload ssh >>"$LOG_FILE" 2>&1 \
      || systemctl reload sshd >>"$LOG_FILE" 2>&1 \
      || true

    die "Could not reload SSH. Hardening config was removed."
  fi

  key=""
  SSH_PUBKEY=""
}


detect_legacy_installation() {
  LEGACY_PRESENT=0
  if [[ -s "$PM_DB" ]]; then
    local existing_users
    existing_users=$(sqlite3 "$PM_DB" 'SELECT COUNT(*) FROM users;' 2>/dev/null || echo 0)
    (( existing_users > 0 )) && return 0
  fi
  if [[ -f "$XRAY_CONF" ]]; then
    local count
    count=$(jq -r '[.inbounds[]? | select(.protocol == "vless") | .settings.clients[]? | .id] | length' "$XRAY_CONF" 2>/dev/null || echo 0)
    (( count > 0 )) && LEGACY_PRESENT=1
  fi
  if [[ -f "$HY_CONF" ]] && grep -Eq '^[[:space:]]*type:[[:space:]]*password[[:space:]]*$' "$HY_CONF"; then
    LEGACY_PRESENT=1
  fi
  if (( LEGACY_PRESENT == 1 && KEEP_SECRETS == 0 )); then
    die "Legacy single-user VPN credentials were detected. Re-run with -s/--keep-secrets so REALITY/TLS server secrets stay unchanged during migration."
  fi
}

resolve_snis() {
  section "Resolving Xray and Hysteria SNI"
  XRAY_SNI="${XSNI:-${SNI:-${OLD_XRAY_SNI:-}}}"
  HY_SNI="${HSNI:-${SNI:-${OLD_HY_SNI:-}}}"

  if [[ -z "$XRAY_SNI" || -z "$HY_SNI" ]]; then
    pick_sni
    [[ -n "$XRAY_SNI" ]] || XRAY_SNI="$SNI"
    [[ -n "$HY_SNI" ]] || HY_SNI="$SNI"
  fi

  validate_hostname "Xray SNI" "$XRAY_SNI"
  validate_hostname "Hysteria SNI" "$HY_SNI"

  if (( XSNI_EXPLICIT == 1 )); then
    check_tls "$XRAY_SNI" "$XRAY_SNI" || die "Explicit Xray SNI failed TLS 1.3 + HTTP/2 validation: $XRAY_SNI"
  fi
  if (( HSNI_EXPLICIT == 1 )); then
    check_tls "$HY_SNI" "$HY_SNI" || die "Explicit Hysteria SNI failed TLS 1.3 + HTTP/2 validation: $HY_SNI"
  fi
  log "Xray SNI: $XRAY_SNI"
  log "Hysteria SNI: $HY_SNI"
}

install_proxy_manager() {
  section "Installing Proxy Manager"
  local src_bin=""

  if [[ -n "$PROXY_MANAGER_BIN" ]]; then
    [[ -x "$PROXY_MANAGER_BIN" ]] || die "PROXY_MANAGER_BIN is not executable: $PROXY_MANAGER_BIN"
    src_bin="$PROXY_MANAGER_BIN"
  elif [[ -f "$PROXY_MANAGER_SOURCE/go.mod" && -f "$PROXY_MANAGER_SOURCE/Makefile" ]]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y golang-go nodejs npm make gcc libc6-dev libsqlite3-dev >>"$LOG_FILE" 2>&1
    (cd "$PROXY_MANAGER_SOURCE" && make build) >>"$LOG_FILE" 2>&1 \
      || die "Proxy Manager source build failed. Provide PROXY_MANAGER_BIN=/path/to/prebuilt/binary if this VPS lacks a compatible Node/Go toolchain."
    src_bin="$PROXY_MANAGER_SOURCE/bin/proxy-manager"
  elif [[ -x "$PM_BIN" ]]; then
    src_bin="$PM_BIN"
    log "Reusing already installed Proxy Manager binary."
  else
    die "Proxy Manager binary/source not found. Run this script from the proxy-manager project or set PROXY_MANAGER_BIN/PROXY_MANAGER_SOURCE."
  fi

  getent group "$PM_GROUP" >/dev/null || groupadd --system "$PM_GROUP"
  id "$PM_USER" >/dev/null 2>&1 || useradd --system --gid "$PM_GROUP" --home-dir "$PM_STATE" --shell /usr/sbin/nologin "$PM_USER"
  install -d -o root -g "$PM_GROUP" -m 0750 "$PM_ETC"
  install -d -o "$PM_USER" -g "$PM_GROUP" -m 0700 "$PM_STATE" "$PM_LOG"
  if [[ "$src_bin" != "$PM_BIN" ]]; then install -o root -g root -m 0755 "$src_bin" "$PM_BIN"; fi

  cat >/etc/systemd/system/proxy-manager.service <<'EOF_PM_SERVICE'
[Unit]
Description=Proxy Manager
After=network-online.target xray.service
Wants=network-online.target
Before=hysteria-server.service

[Service]
Type=simple
User=proxy-manager
Group=proxy-manager
ExecStart=/usr/local/bin/proxy-manager --config /etc/proxy-manager/config.yaml serve
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ProtectClock=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
ReadWritePaths=/var/lib/proxy-manager /var/log/proxy-manager
ReadOnlyPaths=/etc/proxy-manager
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF_PM_SERVICE
  chmod 644 /etc/systemd/system/proxy-manager.service
  log "Proxy Manager binary and systemd unit installed."
}

write_proxy_manager_config() {
  section "Writing Proxy Manager configuration"
  local pin
  pin=$(printf '%s' "$HY_PIN" | tr -d ':' | tr 'A-F' 'a-f')
  cat >"$PM_CONFIG" <<EOF_PM_CONFIG
listen: 127.0.0.1:18080
internal_listen: 127.0.0.1:18081
database: $PM_DB
session_secure: false
session_ttl: 12h
public_ip: $PUBLIC_IP
port: $PORT

xray:
  api_address: 127.0.0.1:10085
  inbound_tag: vless-reality
  sni: $XRAY_SNI
  public_key: $PUB
  short_id: $SHORT_ID
  flow: xtls-rprx-vision

hysteria:
  sni: $HY_SNI
  certificate_pin: $pin
  insecure: true

services:
  xray: xray.service
  hysteria: hysteria-server.service
EOF_PM_CONFIG
  chown root:"$PM_GROUP" "$PM_CONFIG"
  chmod 0640 "$PM_CONFIG"
}

prompt_pm_admin_password() {
  local p1 p2
  if [[ -n "$PM_ADMIN_PASSWORD" ]]; then printf '%s' "$PM_ADMIN_PASSWORD"; return 0; fi
  [[ -r /dev/tty && -w /dev/tty ]] || die "First Proxy Manager install requires PM_ADMIN_PASSWORD in non-interactive mode."
  echo >/dev/tty; echo "Configure Proxy Manager web admin '$PM_ADMIN_USERNAME' (minimum 14 characters)." >/dev/tty
  while true; do
    printf 'Proxy Manager password: ' >/dev/tty; IFS= read -r -s p1 </dev/tty || die "Could not read Proxy Manager password."; printf '\n' >/dev/tty
    printf 'Repeat password: ' >/dev/tty; IFS= read -r -s p2 </dev/tty || die "Could not read Proxy Manager password."; printf '\n' >/dev/tty
    (( ${#p1} >= 14 )) || { warn "Proxy Manager password must be at least 14 characters."; continue; }
    [[ "$p1" == "$p2" ]] || { warn "Passwords do not match."; continue; }
    printf '%s' "$p1"; return 0
  done
}

initialize_proxy_manager() {
  section "Initializing Proxy Manager database"
  local need_admin=0 admin_password password_file
  if [[ ! -s "$PM_DB" ]]; then need_admin=1; fi
  if (( need_admin == 1 )); then
    admin_password=$(prompt_pm_admin_password)
    password_file=$(mktemp "$PM_STATE/.admin-password.XXXXXX")
    chown "$PM_USER:$PM_GROUP" "$password_file"; chmod 0600 "$password_file"
    printf '%s' "$admin_password" >"$password_file"; admin_password=""
    sudo -u "$PM_USER" "$PM_BIN" --config "$PM_CONFIG" init-admin --username "$PM_ADMIN_USERNAME" --password-file "$password_file" >>"$LOG_FILE" 2>&1
    rm -f "$password_file"
    log "Proxy Manager admin initialized."
  else
    log "Existing Proxy Manager database preserved."
  fi
  chown "$PM_USER:$PM_GROUP" "$PM_DB"* 2>/dev/null || true
  chmod 0600 "$PM_DB"* 2>/dev/null || true
}

migrate_legacy_if_needed() {
  (( LEGACY_PRESENT == 1 )) || return 0
  section "Migrating legacy single-user credentials"
  local count
  count=$(sqlite3 "$PM_DB" 'SELECT COUNT(*) FROM users;' 2>/dev/null || echo 0)
  if (( count > 0 )); then
    log "Proxy Manager already contains users; legacy import skipped."
    return 0
  fi
  sudo -u "$PM_USER" "$PM_BIN" --config "$PM_CONFIG" migrate-legacy \
    --xray-config "$XRAY_CONF" --hysteria-config "$HY_CONF" >>"$LOG_FILE" 2>&1 \
    || die "Legacy migration failed; original daemon configurations have not been replaced."
  INITIAL_ACCOUNT="legacy"
  log "Legacy account imported. Original UUID/password are retained in SQLite."
}

ensure_initial_account() {
  local count
  count=$(sqlite3 "$PM_DB" 'SELECT COUNT(*) FROM users;' 2>/dev/null || echo 0)
  if (( count == 0 )); then
    "$PM_BIN" --config "$PM_CONFIG" user add default >>"$LOG_FILE" 2>&1 \
      || die "Could not create initial Proxy Manager account."
    INITIAL_ACCOUNT="default"
    log "Created initial account: default"
  fi
}

load_existing_secrets() {
  section "Loading existing server-level VPN secrets"
  [[ -f "$XRAY_CONF" ]] || die "Cannot use -s: existing Xray config was not found: $XRAY_CONF"
  [[ -f "$HY_CONF" ]] || die "Cannot use -s: existing Hysteria config was not found: $HY_CONF"
  [[ -f "$HY_DIR/server.crt" ]] || die "Cannot use -s: existing Hysteria certificate was not found."
  [[ -f "$HY_DIR/server.key" ]] || die "Cannot use -s: existing Hysteria private key was not found."

  PRIV=$(jq -r '[.inbounds[] | select(.protocol == "vless" and .streamSettings.security == "reality") | .streamSettings.realitySettings.privateKey][0] // empty' "$XRAY_CONF")
  SHORT_ID=$(jq -r '[.inbounds[] | select(.protocol == "vless" and .streamSettings.security == "reality") | .streamSettings.realitySettings.shortIds[0]][0] // empty' "$XRAY_CONF")
  OLD_XRAY_SNI=$(jq -r '[.inbounds[] | select(.protocol == "vless" and .streamSettings.security == "reality") | .streamSettings.realitySettings.serverNames[0]][0] // empty' "$XRAY_CONF")

  [[ -n "$PRIV" ]] || die "Cannot use -s: could not read existing REALITY private key."
  [[ -n "$SHORT_ID" ]] || die "Cannot use -s: could not read existing REALITY short ID."
  [[ -n "$OLD_XRAY_SNI" ]] || die "Cannot use -s: could not read existing Xray SNI."

  local kp
  kp=$(xray x25519 -i "$PRIV" 2>&1) || die "Could not derive REALITY public key."
  PUB=$(grep -iE '^(public|password)' <<<"$kp" | awk '{print $NF}' | head -n1)
  [[ -n "$PUB" ]] || die "Could not parse REALITY public key."

  OLD_HY_SNI=$(awk '
    /^[[:space:]]*url:[[:space:]]*https:\/\// {
      s=$0; sub(/^[[:space:]]*url:[[:space:]]*https:\/\//,"",s); sub(/[\/#].*$/,"",s); print s; exit
    }
  ' "$HY_CONF")
  if [[ -z "$OLD_HY_SNI" ]]; then
    OLD_HY_SNI=$(openssl x509 -in "$HY_DIR/server.crt" -noout -subject -nameopt RFC2253 2>/dev/null | sed -n 's/^subject=CN=//p' | head -n1)
  fi
  [[ -n "$OLD_HY_SNI" ]] || OLD_HY_SNI="$OLD_XRAY_SNI"

  HY_PIN=$(openssl x509 -noout -fingerprint -sha256 -in "$HY_DIR/server.crt" | cut -d= -f2)
  [[ -n "$HY_PIN" ]] || die "Could not calculate Hysteria certificate fingerprint."

  log "Existing REALITY key/short ID and Hysteria certificate/key loaded."
  log_raw "[$(date -Is)] [INFO] Server secret mode: preserve"
}

gen_secrets() {
  section "Generating server-level VPN secrets"
  SHORT_ID=$(openssl rand -hex 8)
  local kp
  kp=$(xray x25519 2>&1)
  PRIV=$(grep -iE '^private' <<<"$kp" | awk '{print $NF}')
  PUB=$(grep -iE '^(public|password)' <<<"$kp" | awk '{print $NF}')
  [[ -n "$PRIV" && -n "$PUB" ]] || die "Could not parse xray x25519 output."
  log "REALITY server credentials generated. User credentials will be generated by Proxy Manager."
}

write_xray() {
  section "Writing Xray configuration"
  mkdir -p "$(dirname "$XRAY_CONF")"
  cat >"$XRAY_CONF" <<EOF_XRAY
{
  "log": { "loglevel": "warning" },
  "api": {
    "tag": "api",
    "listen": "127.0.0.1:10085",
    "services": ["HandlerService", "StatsService"]
  },
  "inbounds": [
    {
      "tag": "vless-reality",
      "listen": "0.0.0.0",
      "port": $PORT,
      "protocol": "vless",
      "settings": {
        "clients": [],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "$XRAY_SNI:$DONOR_PORT",
          "xver": 0,
          "serverNames": ["$XRAY_SNI"],
          "privateKey": "$PRIV",
          "shortIds": ["$SHORT_ID"]
        }
      },
      "sniffing": { "enabled": true, "destOverride": ["http", "tls", "quic"] }
    }
  ],
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" },
    { "protocol": "blackhole", "tag": "block" }
  ]
}
EOF_XRAY
  if getent group xray >/dev/null 2>&1; then chown root:xray "$XRAY_CONF"; chmod 640 "$XRAY_CONF"; else chown root:root "$XRAY_CONF"; chmod 600 "$XRAY_CONF"; fi
  xray run -test -config "$XRAY_CONF" >>"$LOG_FILE" 2>&1 || die "Xray rejected the configuration."
  log "Xray configuration validated; API is loopback-only on 127.0.0.1:10085."
}

write_hysteria() {
  section "Writing Hysteria configuration"
  mkdir -p "$HY_DIR"
  if (( KEEP_SECRETS == 0 )); then
    log "Generating a new Hysteria TLS certificate."
    openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
      -keyout "$HY_DIR/server.key" -out "$HY_DIR/server.crt" -subj "/CN=$HY_SNI" -days 3650 >>"$LOG_FILE" 2>&1
  else
    log "Keeping the existing Hysteria TLS certificate and private key."
  fi
  HY_PIN=$(openssl x509 -noout -fingerprint -sha256 -in "$HY_DIR/server.crt" | cut -d= -f2)
  [[ -n "$HY_PIN" ]] || die "Could not calculate Hysteria certificate fingerprint."

  cat >"$HY_CONF" <<EOF_HY
listen: :$PORT

tls:
  cert: $HY_DIR/server.crt
  key: $HY_DIR/server.key

auth:
  type: http
  http:
    url: http://127.0.0.1:18081/internal/hysteria/auth
    insecure: false

masquerade:
  type: proxy
  proxy:
    url: https://$HY_SNI
    rewriteHost: true
EOF_HY
  if id hysteria >/dev/null 2>&1; then chown -R hysteria:hysteria "$HY_DIR"; fi
  chmod 600 "$HY_DIR/server.key"
  log "Hysteria configured to authenticate through Proxy Manager on loopback."
}

start_services() {
  section "Starting Proxy Manager and VPN services"
  systemctl daemon-reload >>"$LOG_FILE" 2>&1
  systemctl enable xray hysteria-server proxy-manager >>"$LOG_FILE" 2>&1

  systemctl restart xray >>"$LOG_FILE" 2>&1
  sleep 1
  systemctl restart proxy-manager >>"$LOG_FILE" 2>&1
  sleep 1

  curl -fsS http://127.0.0.1:18081/internal/health >/dev/null \
    || die "Proxy Manager internal health endpoint is unavailable."

  "$PM_BIN" --config "$PM_CONFIG" reconcile >>"$LOG_FILE" 2>&1 \
    || die "Initial Xray reconciliation failed."

  systemctl restart hysteria-server >>"$LOG_FILE" 2>&1
  sleep 2

  local s
  for s in proxy-manager xray hysteria-server; do
    if ! systemctl is-active --quiet "$s"; then
      journalctl -u "$s" -n 100 --no-pager >>"$LOG_FILE" 2>&1 || true
      die "Service $s failed to start."
    fi
    log "Service $s is active."
  done
}

tune_net() {
  section "Configuring network tuning"

  modprobe tcp_bbr >>"$LOG_FILE" 2>&1 || true

  if grep -qw \
      bbr \
      /proc/sys/net/ipv4/tcp_available_congestion_control \
      2>/dev/null
  then
    cat >/etc/sysctl.d/99-bbr.conf <<'EOF_BBR'
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF_BBR

    sysctl \
      -p /etc/sysctl.d/99-bbr.conf \
      >>"$LOG_FILE" 2>&1 \
      || true

    log "BBR enabled."
  else
    warn "BBR is unavailable. Skipping."
  fi
}

setup_firewall() {
  section "Configuring firewall"

  if [[ "$SKIP_FIREWALL" == 1 ]]; then
    warn "Firewall setup skipped because SKIP_FIREWALL=1."
    return 0
  fi

  local ports=()
  local p

  mapfile -t ports < <(
    sshd -T 2>/dev/null \
      | awk '/^port /{print $2}'
  )

  [[ -n "${SSH_CONNECTION:-}" ]] \
    && ports+=("${SSH_CONNECTION##* }")

  [[ ${#ports[@]} -gt 0 ]] \
    || ports=(22)

  ufw default deny incoming \
    >>"$LOG_FILE" 2>&1

  ufw default allow outgoing \
    >>"$LOG_FILE" 2>&1

  for p in $(
    printf '%s\n' "${ports[@]}" \
      | sort -u
  ); do
    ufw allow "$p/tcp" \
      >>"$LOG_FILE" 2>&1
  done

  ufw allow "$PORT/tcp" \
    >>"$LOG_FILE" 2>&1

  ufw allow "$PORT/udp" \
    >>"$LOG_FILE" 2>&1

  if ! ufw --force enable >>"$LOG_FILE" 2>&1; then
    warn "ufw could not be enabled."
    return 0
  fi

  log "ufw enabled."

  ufw status verbose \
    >>"$LOG_FILE" 2>&1 \
    || true
}

print_links() {
  section "Proxy Manager access and initial links"
  local user_home links_file
  user_home=$(getent passwd "$NEW_USER" | cut -d: -f6)
  [[ -n "$user_home" && -d "$user_home" ]] || die "Could not determine home directory for $NEW_USER."
  links_file="$user_home/proxy-links.txt"

  if [[ -n "$INITIAL_ACCOUNT" ]]; then
    umask 077
    "$PM_BIN" --config "$PM_CONFIG" links "$INITIAL_ACCOUNT" >"$links_file"
    chown "$NEW_USER:$(id -gn "$NEW_USER")" "$links_file"
    chmod 600 "$links_file"
    echo >&2
    echo "Initial account: $INITIAL_ACCOUNT" >&2
    cat "$links_file" >&2
    echo >&2
    echo "Links saved to: $links_file" >&2
  else
    log "Existing Proxy Manager database preserved; no user credentials were regenerated."
  fi

  echo >&2
  echo "Proxy Manager is bound to localhost only." >&2
  echo "Open it through SSH forwarding:" >&2
  echo "  ssh -L 8080:127.0.0.1:18080 $NEW_USER@$PUBLIC_IP" >&2
  echo "Then open: http://127.0.0.1:8080" >&2
  echo >&2
  echo "CLI users:" >&2
  "$PM_BIN" --config "$PM_CONFIG" user list >&2 || true
  log_raw "[$(date -Is)] [INFO] Proxy Manager access instructions printed."
}

final_diagnostics() {
  section "Final system diagnostics"
  {
    echo; echo "===== FINAL STATE ====="
    echo "Timestamp: $(date -Is)"
    echo "Public IP: $PUBLIC_IP"; echo "Port: $PORT"
    echo "Xray SNI: $XRAY_SNI"; echo "Hysteria SNI: $HY_SNI"
    echo "Keep server secrets: $KEEP_SECRETS"
    echo; echo "--- listening ports ---"; ss -lntup 2>&1 || true
    echo; echo "--- service states ---"
    systemctl is-active proxy-manager 2>&1 || true
    systemctl is-active xray 2>&1 || true
    systemctl is-active hysteria-server 2>&1 || true
    echo; echo "--- Proxy Manager health ---"; curl -fsS http://127.0.0.1:18081/internal/health 2>&1 || true
    echo; echo "--- Xray API reconcile ---"; "$PM_BIN" --config "$PM_CONFIG" reconcile 2>&1 || true
    echo; echo "--- SSH effective auth settings ---"
    sshd -T 2>/dev/null | grep -E '^(port|pubkeyauthentication|passwordauthentication|kbdinteractiveauthentication|permitrootlogin) ' || true
    echo; echo "--- UFW ---"; ufw status verbose 2>&1 || true
    echo "===== END FINAL STATE ====="
  } >>"$LOG_FILE"
  log "Final diagnostics written to log."
}

section "proxy-setup.sh started"
log_raw "[$(date -Is)] [INFO] Script PID: $$"
log_raw "[$(date -Is)] [INFO] Shell: $BASH_VERSION"
log_raw "[$(date -Is)] [INFO] Hostname: $(hostname 2>/dev/null || true)"
log_raw "[$(date -Is)] [INFO] Architecture: $(uname -m)"
log_raw "[$(date -Is)] [INFO] Requested VPN port: $PORT"
log_raw "[$(date -Is)] [INFO] Keep server secrets: $KEEP_SECRETS"

install_deps
PUBLIC_IP=$(detect_ip) || die "Could not detect the public IPv4 address."
log "Public IP: $PUBLIC_IP"

detect_legacy_installation
install_xray
install_hysteria
stop_services
check_ports
backup_existing

if (( KEEP_SECRETS == 1 )); then
  load_existing_secrets
fi

resolve_snis

if (( KEEP_SECRETS == 0 )); then
  gen_secrets
fi

create_admin_user
setup_ssh_key_auth

# We need the Hysteria certificate fingerprint in Proxy Manager config. Generate
# or preserve the certificate before writing that config, while legacy daemon
# configs are still intact for migration.
if (( KEEP_SECRETS == 0 )); then
  mkdir -p "$HY_DIR"
  openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
    -keyout "$HY_DIR/server.key" -out "$HY_DIR/server.crt" -subj "/CN=$HY_SNI" -days 3650 >>"$LOG_FILE" 2>&1
  HY_PIN=$(openssl x509 -noout -fingerprint -sha256 -in "$HY_DIR/server.crt" | cut -d= -f2)
fi

install_proxy_manager
write_proxy_manager_config
initialize_proxy_manager
migrate_legacy_if_needed

# From this point SQLite is authoritative. Static legacy users are deliberately
# removed from Xray and Hysteria delegates auth to Proxy Manager.
write_xray

# write_hysteria normally creates a certificate on non--s runs; it was already
# generated above so temporarily treat TLS material as preserved for this call.
if (( KEEP_SECRETS == 0 )); then
  _keep="$KEEP_SECRETS"; KEEP_SECRETS=1; write_hysteria; KEEP_SECRETS="$_keep"
else
  write_hysteria
fi

start_services
ensure_initial_account
"$PM_BIN" --config "$PM_CONFIG" reconcile >>"$LOG_FILE" 2>&1 || die "Xray reconcile after initial user creation failed."
tune_net
setup_firewall
print_links
final_diagnostics

section "Installation completed successfully"
echo >&2
echo "Setup completed successfully." >&2
echo "Log file: $LOG_FILE" >&2
echo "Latest log: $LOG_ROOT/latest.log" >&2
echo "Proxy Manager DB: $PM_DB" >&2
echo >&2
echo "Before closing the current SSH session, test a second login:" >&2
echo "  ssh $NEW_USER@$PUBLIC_IP" >&2
echo >&2
echo "Proxy Manager tunnel:" >&2
echo "  ssh -L 8080:127.0.0.1:18080 $NEW_USER@$PUBLIC_IP" >&2
