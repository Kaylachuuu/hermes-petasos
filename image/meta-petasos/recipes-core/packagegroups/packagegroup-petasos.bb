SUMMARY = "Petasos runtime"
DESCRIPTION = "BSP runtime, the kiosk screen, the petasos program, and the networking and audio it needs, with ffmpeg for an iPod's music."
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

inherit packagegroup

PACKAGES = "${PN}"

RDEPENDS:${PN} = " \
    packagegroup-superbird-runtime \
    \
    petasos-remote \
    petasos-screen \
    \
    mesa \
    weston \
    blank-cursor \
    cursor-suppress \
    superbird-fbpaint \
    superbird-weston-init-kiosk \
    chromium-ozone-wayland \
    chromium-kiosk \
    \
    bluealsa \
    bluealsa-aplay \
    wireguard-tools \
    iproute2 \
    \
    ffmpeg \
    \
    evtest \
    htop \
"
