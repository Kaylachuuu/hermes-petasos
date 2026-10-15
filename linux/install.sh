#!/bin/sh
# Installs the Petasos desktop app for you on Linux (a Raspberry Pi with a desktop, say): the
# program in ~/.local/share/petasos, and Petasos in the desktop's app menu. Run it from this
# checkout; run it again after `git pull` to update it, though the app also updates itself
# from the Petasos plugin on Hermes.
set -e
cd "$(dirname "$0")/.."

case "$(uname -m)" in
    aarch64|arm64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) echo "No Petasos build for $(uname -m)." >&2; exit 1 ;;
esac
bin="client/dist/petasos-linux-$arch"
if [ ! -f "$bin" ]; then
    echo "No $bin here: build it with client/build.sh (needs Go)." >&2
    exit 1
fi

data="${XDG_DATA_HOME:-$HOME/.local/share}"
dest="$data/petasos"
mkdir -p "$dest" "$data/applications" "$data/icons/hicolor/scalable/apps"
# A copy in use can't be overwritten in place; put the new one beside it and swap.
install -m 755 "$bin" "$dest/petasos.new"
mv -f "$dest/petasos.new" "$dest/petasos"
install -m 644 mac/icon/petasos-icon.svg "$data/icons/hicolor/scalable/apps/petasos.svg"
cat > "$data/applications/petasos.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Petasos
Comment=Talk to your Hermes agent
Exec="$dest/petasos" desktop
Icon=petasos
Terminal=false
Categories=AudioVideo;Utility;
StartupWMClass=Petasos
DESKTOP
command -v update-desktop-database >/dev/null && update-desktop-database "$data/applications" 2>/dev/null || true
command -v kbuildsycoca6 >/dev/null && kbuildsycoca6 >/dev/null 2>&1 || true

echo "Installed Petasos: find it in the app menu, or run $dest/petasos desktop"
found=""
for b in chromium chromium-browser google-chrome google-chrome-stable brave-browser microsoft-edge vivaldi; do
    command -v "$b" >/dev/null && found="$b" && break
done
[ -n "$found" ] || echo "It shows best in Chromium's app window: sudo snap install chromium (or apt install chromium-browser)"
command -v pactl >/dev/null || echo "For sound and volume it needs PulseAudio's tools: sudo apt install pulseaudio-utils"
