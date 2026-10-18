# usbmuxd's service drops to the usbmux user, which meta-oe's recipe doesn't create. It keeps the
# iPhone's trust (pairing) in /var/lib/lockdown, which survives on /var.
inherit useradd

USERADD_PACKAGES = "${PN}"
USERADD_PARAM:${PN} = "--system --no-create-home --home-dir /var/lib/lockdown --shell /bin/false --user-group usbmux"
