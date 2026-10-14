#!/bin/sh
# Report what the Car Thing offers a petasos program: storage, startup, tools, audio.
# Read-only except for one test file it creates and removes in each writable-looking place.
#   ssh root@nocturne.local 'sh -s' < tools/probe_device.sh

echo "== System"
uname -a
cat /etc/os-release 2>/dev/null | grep -E '^(PRETTY_NAME|VERSION_ID)='
cat /etc/nocturne/floor-version 2>/dev/null && echo "(nocturne floor version)"
echo "pid 1: $(cat /proc/1/comm)"
free -m 2>/dev/null | head -2
echo
echo "== Mounts"
grep -E ' (/|/var|/var/lib/bandaid|/etc|/opt[^ ]*|/tmp|/run|/boot[^ ]*|/mnt/[^ ]*) ' /proc/mounts
cat /proc/cmdline; echo
echo
echo "== Can we write here?"
for d in /etc /etc/systemd/system /usr/local /opt /var/lib /var/local /var/lib/bandaid /root; do
    if [ -d "$d" ] && touch "$d/.petasos-probe" 2>/dev/null; then
        rm -f "$d/.petasos-probe"; echo "  yes  $d"
    else
        echo "  no   $d"
    fi
done
df -h / /var /var/lib/bandaid 2>/dev/null
echo
echo "== Startup"
systemctl --version 2>/dev/null | head -1
systemctl list-unit-files --no-pager 2>/dev/null | grep -i -E 'nocturne|superbird|chromium|weston|kiosk|bluetooth|bluealsa|pipewire|pulse' 
ls /etc/systemd/system 2>/dev/null | head -30
echo
echo "== Tools"
for t in curl wget openssl busybox python3 node arecord aplay amixer bluetoothctl bluealsa bluealsa-aplay pw-cat pactl gst-launch-1.0 ffmpeg jq nc evtest mount fw_printenv; do
    p=$(command -v "$t" 2>/dev/null); echo "  ${p:-(none)}  $t"
done
echo
echo "== Audio"
cat /proc/asound/cards 2>/dev/null
ls /dev/snd 2>/dev/null
echo
echo "== Boot partitions"
ls -l /dev/disk/by-partlabel 2>/dev/null
echo
echo "== Processes holding the buttons"
for p in /proc/[0-9]*; do
    ls -l "$p/fd" 2>/dev/null | grep -q '/dev/input/event[01]' && echo "  $(cat "$p/comm") (pid ${p#/proc/})"
done
echo "== Done"
