#!/bin/sh
# Build Petasos.app and install it in /Applications (or ~/Applications when /Applications isn't
# writable without an administrator), replacing an older copy, then start it.
#
#   ./install.sh            build, install and open
#   ./install.sh --no-build install the Petasos.app already built here
set -e
cd "$(dirname "$0")"
[ "$1" = "--no-build" ] || ./build.sh
[ -d Petasos.app ] || { echo "No Petasos.app here; run ./build.sh first." >&2; exit 1; }

dest=/Applications
if [ ! -w "$dest" ]; then
    dest="$HOME/Applications"
    mkdir -p "$dest"
fi

# Quit a running copy first, so it isn't replaced under itself.
if pgrep -x Petasos >/dev/null 2>&1; then
    echo "Quitting Petasos…"
    osascript -e 'tell application id "com.kaylachuuu.petasos" to quit' >/dev/null 2>&1 || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        pgrep -x Petasos >/dev/null 2>&1 || break
        sleep 0.5
    done
    pkill -x Petasos 2>/dev/null || true
fi

rm -rf "$dest/Petasos.app"
ditto Petasos.app "$dest/Petasos.app"
xattr -dr com.apple.quarantine "$dest/Petasos.app" 2>/dev/null || true
echo "Installed $dest/Petasos.app"
open "$dest/Petasos.app"
