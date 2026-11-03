Proxy Manager v2.4 bundle

Install / update on an existing server:
  chmod +x *.sh vpn-manager
  sudo ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

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
Xray is pinned to v26.3.27 by default because sing-box 1.14.x REALITY clients are incompatible with Xray-core v26.9.8+ ML-KEM ClientHello requirements. Override with XRAY_VERSION only after confirming upstream compatibility.
