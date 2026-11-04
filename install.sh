#!/bin/bash
# Build esimctl, install it to ~/.local/bin and the Omarchy bar widget to
# ~/.config/omarchy/plugins. The udev rule needs root and is installed once.
set -euo pipefail
cd "$(dirname "$0")"

go build -trimpath -ldflags "-s -w" -o esimctl ./cmd/esimctl
install -Dm755 esimctl ~/.local/bin/esimctl

mkdir -p ~/.config/omarchy/plugins/andrea.esim
cp omarchy/andrea.esim/* ~/.config/omarchy/plugins/andrea.esim/

if ! cmp -s contrib/70-esimctl.rules /etc/udev/rules.d/70-esimctl.rules; then
  echo "Installing udev rule (needs root)…"
  pkexec sh -c "install -m644 '$PWD/contrib/70-esimctl.rules' /etc/udev/rules.d/ && udevadm control --reload && udevadm trigger --subsystem-match=wwan"
fi

if command -v omarchy >/dev/null && ! grep -q '"andrea.esim"' ~/.config/omarchy/shell.json 2>/dev/null; then
  omarchy-shell shell rescanPlugins >/dev/null 2>&1 || true
  sleep 1
  omarchy bar put andrea.esim --after andrea.network || omarchy bar put andrea.esim
fi
echo "Done. Run 'esimctl tui' or click the SIM icon in the bar."
