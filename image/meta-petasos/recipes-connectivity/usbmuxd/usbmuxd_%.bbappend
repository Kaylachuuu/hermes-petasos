# usbmuxd's service drops to the usbmux user, which meta-oe's recipe doesn't create. It keeps the
# iPhone's trust (pairing) in /var/lib/lockdown, which survives on /var.
inherit useradd

USERADD_PACKAGES = "${PN}"
USERADD_PARAM:${PN} = "--system --no-create-home --home-dir /var/lib/lockdown --shell /bin/false --user-group usbmux"

# meta-oe builds usbmuxd without its preflight worker, which is what pairs with an iPhone and saves
# the pairing. Without it nothing is saved, so the iPhone asked to trust the Car Thing every time it
# was plugged in. The worker needs libimobiledevice (whose recipe's usbmuxd dependency is dropped in
# its bbappend, as it only needs libusbmuxd).
EXTRA_OECONF:remove = "--without-preflight"
DEPENDS += "libimobiledevice"
