Proxy Manager v2.5.3 — Telegram import button

The Telegram platform messages now include a separate "🚀 Импортировать" inline button.
Telegram only accepts HTTP(S)/tg:// URL buttons, so this links to a simple HTTPS landing page at
https://<public-host>/sub/<TOKEN>/<platform>?import=1
The landing page has a big user-activated sing-box://import-remote-profile link.
Manual Source and Import code blocks remain available; QR unchanged.

To upgrade ONLY Telegram + subscription backend (Xray/Hysteria unaffected):
  tar -xzf proxy-manager-v2.5.3-bundle.tar.gz
  cd proxy-manager-v2.5.3-bundle
  sudo ./upgrade-telegram-only.sh

IMPORTANT: Some Telegram in-app browsers refuse to hand off custom schemes. If the import button
on the landing page does not open sing-box, use "Open in browser" in Telegram then tap again.
On some devices OS-level sing-box URI handlers may not be registered; use the Source URL manually.

This update does not rotate VPN secrets or subscription tokens.
