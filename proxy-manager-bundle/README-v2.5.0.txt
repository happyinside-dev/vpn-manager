proxy-manager v2.5.0 — Telegram management MVP
================================================

This release keeps the stable v2.4.6 networking/profile behavior and adds an
optional Telegram management process using the same vpn-manager binary.

SECURITY MODEL
--------------
- vpn-manager.service stays unprivileged and serves only /health + /sub/...
- vpn-manager-telegram.service runs as root because account management must
  update Xray/Hysteria state and restart services.
- Telegram authorization uses numeric Telegram user IDs, never usernames.
- Unknown Telegram users receive no server/account information.
- Bot token is stored at /etc/vpn-manager/telegram-token (0600).
- Telegram config is /etc/vpn-manager/telegram.json (0600).
- Audit log: /var/log/proxy-manager/telegram-audit.log; secrets are not logged.
- Subscription URLs remain stable unless rotate_token is explicitly requested.

INSTALL / UPDATE
----------------
Preserve all current VPN credentials and subscription tokens:

  sudo env \
    XRAY_VERSION=v26.6.27 \
    XRAY_SNI=vpn.example.com \
    HY_SNI=bing.com \
    HY_INSECURE=1 \
    VPN_TELEGRAM_BOT_TOKEN='123456:BOT_TOKEN' \
    VPN_TELEGRAM_MASTER_ID='123456789' \
    ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

Multiple masters:

  VPN_TELEGRAM_MASTER_IDS='123456789,987654321'

If Telegram variables are omitted and there is no existing Telegram config,
the VPN/subscription services are updated normally and the Telegram service is
left disabled.

MASTER COMMANDS
---------------
/start, /help
/users
/add NAME
/delete NAME CONFIRM
/enable NAME
/disable NAME
/rotate NAME
/rotate_token NAME CONFIRM
/invite NAME
/unbind NAME
/links NAME
/status
/selftest
/xray
/hysteria

/add creates the VPN account and a one-time 10-minute invitation code.
The user sends:

  /start CODE

The bot binds that numeric Telegram ID to the VPN profile.

USER COMMANDS
-------------
/start, /help
/links
/android
/ios
/macos
/linux
/tv
/universal
/status
/rotate
/rotate_token CONFIRM

/rotate changes only the user's VLESS UUID and Hysteria password. The stable
subscription URL is preserved. The user refreshes Remote Profile and reconnects.

/rotate_token changes the bearer token and therefore invalidates all previous
subscription URLs for that user. The subscription backend is restarted so the
new token is active immediately.

LINK OUTPUT
-----------
For every platform the bot sends:
1. QR image encoding the sing-box import URL.
2. A formatted message:

  Platform

  Source — manual Remote Profile:
  <https://vpn.example.com/sub/TOKEN/android>

  Import — ready sing-box import URL:
  <sing-box://import-remote-profile?...>

QR generation is local via qrencode; no external QR service receives secrets.

FILES
-----
/etc/vpn-manager/users.yaml               users + optional telegram_id
/etc/vpn-manager/telegram-token           Bot API token, 0600
/etc/vpn-manager/telegram.json            bot config/master IDs, 0600
/etc/vpn-manager/telegram-invites.json    short-lived invitation state, 0600
/etc/systemd/system/vpn-manager.service
/etc/systemd/system/vpn-manager-telegram.service
/var/log/proxy-manager/telegram-audit.log

CHECKS
------
  sudo systemctl status vpn-manager vpn-manager-telegram
  sudo journalctl -u vpn-manager-telegram -n 100 --no-pager
  sudo proxy-manager self-test

NOTES
-----
- The Telegram layer currently invokes the existing root proxy-manager account
  operations for Xray/Hysteria mutation. Subscription/profile generation stays
  in Go. This intentionally avoids rewriting the proven v2.4.6 network mutation
  path in the same release as the Telegram UI.
- A future refactor can move those account mutations behind a Go AccountService
  without changing Telegram commands or subscription URLs.
