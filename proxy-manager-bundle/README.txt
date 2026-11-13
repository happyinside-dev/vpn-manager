Proxy Manager v2.4 bundle

Install / update on an existing server:
  chmod +x *.sh vpn-manager
  sudo ./install-with-subscriptions.sh ./proxy-manager.sh -S

Without -S the existing core installer rotates VPN credentials.
Subscription tokens are preserved by vpn-manager sync-users.

After install:
  sudo proxy-manager account add mama
  sudo proxy-manager account show mama

The command prints import links for:
  Universal: /sub/<TOKEN>
  Android:   /sub/<TOKEN>/android
  iOS:       /sub/<TOKEN>/ios
  macOS:     /sub/<TOKEN>/macos
  Linux:     /sub/<TOKEN>/linux
  TV/IPTV:   /sub/<TOKEN>/tv

Android profile:
- full VPN by default;
- Russian/geo-local services bypass VPN;
- Android package-name matching bypasses popular Russian banks, government,
  marketplaces, Yandex/VK/MAX, operators and major Russian streaming apps;
- .ru, .su, .xn--p1ai go direct;
- geoip-ru and Yandex/VK/government rule-sets go direct;
- private and multicast networks go direct for LAN/IPTV compatibility.

TV/IPTV profile:
- same Russian/local bypass rules;
- Android-TV package matching when supported;
- private/multicast traffic goes direct.

Apple profiles cannot match Android package names. iOS/macOS therefore use
Russian domain/IP/rule-set bypass instead of per-app package routing.

Important: app package names and service infrastructure can change. The regex
is intentionally broad, but no static list can guarantee every Russian app.

v2.4.4 compatibility note:
Xray defaults to v26.6.27 for sing-box 1.14.x REALITY compatibility. Releases v26.7.11+ are excluded from the compatibility matrix because of known sing-box/REALITY client-version and later ML-KEM compatibility problems.


v2.4.5: use `sudo proxy-manager xray matrix` to find a locally compatible Xray release without rotating credentials.


v2.4.6: Android/TV profiles no longer depend on remote rule-set downloads at first start. Yandex/VK/government routing rules are embedded inline; .ru/.su/.рф and Android package rules remain direct. This makes a freshly installed sing-box client bootstrap without pre-cached .srs assets.

Traffic statistics in Telegram: see TRAFFIC.md (commands, persistent history,
API setup, and upgrade-traffic.sh for existing installations).

Telegram permissions update (existing installation, Linux x86_64):
  sudo bash ./upgrade-telegram-only.sh

The bundle includes the compiled binary; Go is not required on the server.
The updater backs up the installed binary and sync helper and restarts the enabled
Telegram service. See TELEGRAM-PERMISSIONS.md for permission controls.
