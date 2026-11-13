proxy-manager v2.4.6

- Android/TV sing-box profiles use inline rule-sets for bootstrap-critical Russian service routing.
- No remote .srs downloads are required before a fresh client can start.
- Keeps .ru/.su/.xn--p1ai direct routing and Android package-name direct rules.
- Xray default pin changed to v26.6.27.
- xray matrix excludes v26.7.11+ candidates for sing-box 1.14.x compatibility.
- Existing server credentials, account credentials, and subscription tokens are preserved with -S.
