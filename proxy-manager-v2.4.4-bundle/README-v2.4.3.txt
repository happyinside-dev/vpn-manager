proxy-manager v2.4.3

Fixes remote sing-box rule-set bootstrap on fresh profiles.

Changes:
- Remote rule-sets use an explicit sing-box 1.14+ HTTP client named rules-direct.
- rules-direct is detoured through direct instead of the VPN/proxy selector.
- route.default_http_client = rules-direct.
- Each remote rule-set explicitly sets http_client = rules-direct.
- The HTTP client resolves its download host using dns-bootstrap (local/platform DNS).
- Device-specific endpoints remain:
  /sub/<TOKEN>
  /sub/<TOKEN>/android
  /sub/<TOKEN>/ios
  /sub/<TOKEN>/macos
  /sub/<TOKEN>/linux
  /sub/<TOKEN>/tv
- Android/TV direct-routing package rules are retained.
- Hysteria SNI/insecure and server-side sniGuard fixes from v2.4.2 are retained.

Upgrade an existing installation without rotating VPN credentials:

  sudo XRAY_SNI="vpn.example.com" \
       HY_SNI="bing.com" \
       HY_INSECURE=1 \
       ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

Then refresh the Remote Profile on the device and reconnect once.
