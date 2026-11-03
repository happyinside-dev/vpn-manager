proxy-manager v2.4.4

Changes:
- Pins Xray-core to v26.3.27 by default for sing-box 1.14.x REALITY compatibility.
- Adds XRAY_VERSION environment override.
- Uses official XTLS installer with --version and --force.
- Verifies the installed Xray version after installation.
- Keeps all v2.4.3 subscription/routing/Hysteria fixes.

Recommended update on an existing server:

  sudo XRAY_VERSION=v26.3.27 \
       XRAY_SNI="vpn.example.com" \
       HY_SNI="bing.com" \
       HY_INSECURE=1 \
       ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

-S preserves existing VPN credentials and subscription tokens.
