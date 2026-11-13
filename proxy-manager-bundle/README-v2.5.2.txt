Proxy VPN v2.5.2 — Telegram UI + self-restart fix

UPGRADE ON EXISTING v2.5.x SERVER (preferred; NO VPN core reinstall):
  tar -xzf proxy-manager-v2.5.2-bundle.tar.gz
  cd proxy-manager-v2.5.2-bundle
  sudo ./upgrade-telegram-only.sh
  sudo systemctl status vpn-manager vpn-manager-telegram --no-pager

Changes:
- Fixed subscription-sync restarting vpn-manager-telegram during bot-triggered account mutations. The bot is started only if inactive. During Telegram-only upgrade it is restarted once at the end.
- Master reply keyboard: users, add profile, server status, E2E test.
- User reply keyboard: choose device, current status, rotate credentials (confirmation).
- Inline account cards: list/select, link formats, new invitation code, enable/disable, rotate, delete (confirmation), unbind (confirmation).
- New invitation code can be generated repeatedly from user card; re-issuing cancels prior pending code for the same account. Valid for 10 minutes.
- User can send an invitation code directly in the bot's private chat, without typing /start.
- Platform menu: Android, iOS, macOS, Linux, TV, Universal, All; individual formats still have QR + Source + Import code blocks.
- /links still sends ALL platform links; /platforms shows device picker.
- Bot refuses group chats and re-checks role and ownership on every callback.
- After account add/delete, the bot verifies on-disk account existence to handle partial success without misleading "exit status 1" notifications.
- New upgrade-telegram-only.sh with backup and rollback on failure. Keeps Xray/Hysteria binaries and credentials untouched.

IMPORTANT:
- The Telegram bot and the subscription service are separate processes. Credentials/subscription tokens remain on the VPS.
- The bot is a root-level management process; keep master Telegram IDs and the bot token secure.
- Do not post subscription URLs or QR screenshots publicly. They are bearer credentials.
- Manual full reinstall remains available using install-with-subscriptions.sh ... -S, but is not necessary for this update.
- Telegram API is not available in the build environment. Go unit tests, go vet, and build are checked locally; test real UI and account ops on VPS.
