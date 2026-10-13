#!/bin/sh
# List the Car Thing's input devices, then print every button press and knob turn for a while.
# Runs ON the Car Thing (busybox sh, no Python needed). From the computer it is plugged into:
#
#   ssh root@nocturne.local 'sh -s' < tools/list_inputs.sh          # 60 seconds
#   ssh root@nocturne.local 'sh -s 120' < tools/list_inputs.sh      # 120 seconds
#
# It only reads /dev/input; it changes nothing. Leave the touchscreen alone while it runs, or it
# floods the output.

SECONDS_TO_LISTEN=${1:-60}

echo "== Input devices"
for dev in /sys/class/input/event*; do
    [ -e "$dev" ] || continue
    ev=${dev##*/}
    echo "/dev/input/$ev: $(cat "$dev/device/name" 2>/dev/null)"
done
echo
echo "== Full device table (/proc/bus/input/devices)"
cat /proc/bus/input/devices
echo

# Names for the codes the community device tree assigns (meson-g12a-superbird.dts).
# Anything else prints as its raw number, which is exactly what we want to learn.
key_name() {
    case "$1" in
        1)  echo "KEY_ESC (Back button?)" ;;
        2)  echo "KEY_1 (Preset 1?)" ;;
        3)  echo "KEY_2 (Preset 2?)" ;;
        4)  echo "KEY_3 (Preset 3?)" ;;
        5)  echo "KEY_4 (Preset 4?)" ;;
        28) echo "KEY_ENTER (knob press?)" ;;
        50) echo "KEY_M (Mute?)" ;;
        *)  echo "key $1" ;;
    esac
}

# Reads one 24-byte struct input_event at a time (aarch64: 16-byte timestamp, u16 type,
# u16 code, s32 value) and prints it, skipping the SYN markers between events.
read_events() {
    ev=$1
    label=$(cat "/sys/class/input/${ev##*/}/device/name" 2>/dev/null)
    while :; do
        set -- $(dd if="$ev" bs=24 count=1 2>/dev/null | od -An -v -t u2)
        [ $# -ge 11 ] || { sleep 1; continue; }
        type=$9; code=${10}; value=${11}
        [ "$value" -ge 32768 ] && value=$((value - 65536))
        case "$type" in
            0) continue ;;
            1) case "$value" in 1) what=pressed ;; 0) what=released ;; 2) what=held ;; *) what="value $value" ;; esac
               echo "$label  $(key_name "$code")  $what  [$ev type=1 code=$code]" ;;
            2) case "$code" in 6) axis="REL_HWHEEL (knob turn?)" ;; *) axis="rel axis $code" ;; esac
               echo "$label  $axis  $value  [$ev type=2 code=$code]" ;;
            *) echo "$label  type=$type code=$code value=$value  [$ev]" ;;
        esac
    done
}

echo "== Listening for $SECONDS_TO_LISTEN seconds"
echo "Press each top button (left to right), the front button, press the knob, and turn the knob"
echo "a few clicks each way. Say in your notes which physical control you pressed in which order."
echo
pids=""
for ev in /dev/input/event*; do
    [ -e "$ev" ] || continue
    read_events "$ev" &
    pids="$pids $!"
done
sleep "$SECONDS_TO_LISTEN"
kill $pids 2>/dev/null
echo
echo "== Done"
