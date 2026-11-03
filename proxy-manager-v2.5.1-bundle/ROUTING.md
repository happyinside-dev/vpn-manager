# Platform routing

## Android

The Android profile uses sing-box `package_name_regex`, which is supported by
the Android graphical client. Matching traffic is sent to `direct`; everything
else ends at the `VPN` selector.

The broad application matcher covers brand/package names containing terms such
as Sber, VTB, T-Bank/Tinkoff, Alfa, Gazprombank, Ozon, Wildberries, Yandex,
VK, MAX/oneme, Gosuslugi/Rostel, RuTube, Kinopoisk, IVI, Okko, Wink, Premier,
Smotrim, MTS, Megafon, Beeline, T2/Tele2, MirPay/SBPay, Megamarket, Avito and
2GIS.

Known current package IDs independently verified during implementation include:
- ru.sberbankmobile
- ru.alfabank.mobile.android
- ru.gazprombank.android.mobilebank.app
- ru.vtb24.mobilebanking.android
- ru.rostel
- ru.ozon.app.android
- ru.oneme.app
- com.vkontakte.android

## Russian network bypass

The mobile/TV profiles also send these directly:
- .ru, .su, .xn--p1ai domains;
- Yandex/VK/government remote rule-sets;
- geoip-ru;
- RFC1918/private destinations;
- IPv4/IPv6 multicast, including IPTV/LAN discovery paths.

DNS for the same Russian domain/rule-set groups is sent to 77.88.8.8 directly.
Other DNS uses Cloudflare DoH through the VPN selector.

## Apple

iOS/macOS do not have Android package-name matching, so platform profiles use
the Russian domain/IP/rule-set bypass only.
