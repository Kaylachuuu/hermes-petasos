# libimobiledevice builds against libusbmuxd alone; meta-oe's recipe also lists the usbmuxd daemon,
# which would loop now that usbmuxd is built with libimobiledevice (usbmuxd_%.bbappend).
DEPENDS:remove = "usbmuxd"
