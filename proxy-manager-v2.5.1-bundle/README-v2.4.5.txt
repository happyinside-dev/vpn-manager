proxy-manager v2.4.5

Adds safe Xray compatibility testing without rotating VPN credentials.

Commands:
  sudo proxy-manager xray version
  sudo proxy-manager xray test v26.7.28
  sudo proxy-manager xray matrix

`xray matrix` tests official pre-26.9.8 releases from newest to older. A failed candidate is rolled back to the previously installed Xray version. The first candidate passing the local authenticated VLESS/REALITY E2E test is kept and written to /etc/proxy-manager/xray-version. Future installer runs use that pin unless XRAY_VERSION is explicitly supplied.

The matrix does NOT rotate UUIDs, passwords, REALITY keys, short IDs, or subscription tokens.
