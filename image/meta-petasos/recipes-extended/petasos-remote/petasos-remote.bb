SUMMARY = "Petasos: the Car Thing as a voice remote for Athena"
DESCRIPTION = "The device program from client/ (prebuilt, static), its service, and the network setup for Bluetooth and USB tethering. The rootfs copy is only the starting point: the program copies itself to /var/lib/petasos and updates itself there from the petasos Hermes plugin."
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

FILESEXTRAPATHS:prepend := "${PETASOS_CLIENT_DIST}:"

SRC_URI = " \
    file://petasos-linux-arm64 \
    file://petasos.service \
    file://50-petasos-pan.network \
    file://40-petasos-usb-tether.network \
"
S = "${UNPACKDIR}"

COMPATIBLE_HOST = "aarch64.*-linux"
INSANE_SKIP:${PN} += "already-stripped ldflags"

inherit systemd

SYSTEMD_SERVICE:${PN} = "petasos.service"
SYSTEMD_AUTO_ENABLE = "enable"

# usbmuxd starts an iPhone's USB tethering (and asks it to trust the Car Thing).
RDEPENDS:${PN} += "bluez5 usbmuxd"

do_install() {
    install -d ${D}${libdir}/petasos
    install -m 0755 ${S}/petasos-linux-arm64 ${D}${libdir}/petasos/petasos

    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${S}/petasos.service ${D}${systemd_system_unitdir}/petasos.service

    install -d ${D}${sysconfdir}/systemd/network
    install -m 0644 ${S}/50-petasos-pan.network ${D}${sysconfdir}/systemd/network/50-petasos-pan.network
    install -m 0644 ${S}/40-petasos-usb-tether.network ${D}${sysconfdir}/systemd/network/40-petasos-usb-tether.network
}

FILES:${PN} = " \
    ${libdir}/petasos \
    ${systemd_system_unitdir}/petasos.service \
    ${sysconfdir}/systemd/network/50-petasos-pan.network \
    ${sysconfdir}/systemd/network/40-petasos-usb-tether.network \
"
