SUMMARY = "Petasos image for the Car Thing"
DESCRIPTION = "yocto-superbird with the petasos program, Bluetooth and USB tethering, WireGuard and Bluetooth audio, and a chromium kiosk screen. ext4 read-only rootfs; /var holds petasos's own files."
LICENSE = "MIT"

inherit core-image
inherit superbird-headroom-check
inherit superbird-image
inherit mainline-flashthing

# Root logs in with no password, as on yocto-superbird's own BSP image. The openssh bbappend limits
# that to the USB cable.
IMAGE_FEATURES += " \
    ssh-server-openssh \
    allow-empty-password \
    allow-root-login \
    empty-root-password \
    post-install-logging \
"

IMAGE_INSTALL = " \
    packagegroup-core-boot \
    packagegroup-petasos \
"

BAD_RECOMMENDATIONS += "kernel-modules udev-hwdb wpa-supplicant wireless-regdb wireless-regdb-static weston-init"

SUPERBIRD_ROOTFS_TYPE = "ext4"
EXTRA_IMAGECMD:ext4 = "-i 16384 -m 0 -O ^has_journal -O ^huge_file"
IMAGE_FSTYPES = "wic ext4"
IMAGE_OVERHEAD_FACTOR = "1.0"
IMAGE_ROOTFS_EXTRA_SPACE = "4096"

# No A/B update payloads are built, so there is no OTA headroom to reserve. The OS is updated by
# reflashing; petasos updates itself.
SUPERBIRD_REQUIRE_OTA_HEADROOM = "0"

WKS_FILE = "superbird-mainline.wks.in"

do_image_wic[depends] += " \
    virtual/kernel:do_deploy \
    superbird-uenv:do_deploy \
    superbird-extlinux:do_deploy \
    superbird-logo:do_deploy \
"

EXTRA_IMAGEDEPENDS += "superbird-uboot"
