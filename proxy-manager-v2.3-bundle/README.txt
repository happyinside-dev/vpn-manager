proxy-manager v2.3 subscription bundle
=====================================

Files required in the same directory:
  install-with-subscriptions.sh
  vpn-manager
  your original proxy-manager-v2.2*.sh

Recommended installation:

  chmod +x install-with-subscriptions.sh vpn-manager proxy-manager-v2.2*.sh
  sudo ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh

If exactly one proxy-manager-v2.2*.sh is present, the path can be omitted:

  sudo ./install-with-subscriptions.sh

Installer options are passed through after the core installer path, e.g.:

  sudo ./install-with-subscriptions.sh ./proxy-manager-v2.2.sh -S

After installation, use the same manager command as before:

  sudo proxy-manager account add mama
  sudo proxy-manager account rotate mama
  sudo proxy-manager account disable mama
  sudo proxy-manager account enable mama
  sudo proxy-manager account delete mama

The wrapper automatically synchronizes /etc/vpn-manager/users.yaml while
preserving subscription tokens for existing users.

Useful checks:

  systemctl status vpn-manager
  curl http://127.0.0.1:8080/health
  sudo cat /etc/vpn-manager/users.yaml
  sudo cat /etc/vpn-manager/server.yaml

Public URL pattern:
  https://<XRAY_SNI>/sub/<TOKEN>

Security:
  /sub/ has nginx access_log disabled and Cache-Control: no-store.
  REALITY private key is never copied to server.yaml or client profiles.
