proxy-manager v2.4.1 bundle

Fixes independent Hysteria2 external SNI support.

Recommended existing-server update:

  sudo XRAY_SNI="vpn.example.com" \
       HY_SNI="bing.com" \
       HY_INSECURE=1 \
       ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

Behavior:
- XRAY_SNI remains the real VPS website/REALITY name and must resolve to the VPS.
- HY_SNI may be an external name such as bing.com when HY_INSECURE=1.
- In HY_INSECURE=1 mode the installer does not require HY_SNI DNS to point to the VPS.
- Let's Encrypt certificate is requested only for XRAY_SNI (and only for HY_SNI when Hysteria verification is enabled and it is a local VPS domain).
- state.json persists server.hy_insecure.
- vpn-manager server.yaml receives hysteria2.insecure from state.
- Hysteria self-test uses the same insecure setting.
- generated hysteria2:// links include insecure=1 when enabled.
- -S preserves UUIDs, passwords and REALITY key material while allowing explicit SNI/TLS policy overrides.
