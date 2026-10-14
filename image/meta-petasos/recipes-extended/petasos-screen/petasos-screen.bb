SUMMARY = "Placeholder screen for the petasos kiosk"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

SRC_URI = "file://index.html file://splash.png"
S = "${UNPACKDIR}"

inherit allarch

do_install() {
    install -d ${D}${datadir}/petasos-screen
    install -m 0644 ${S}/index.html ${D}${datadir}/petasos-screen/index.html
    install -m 0644 ${S}/splash.png ${D}${datadir}/petasos-screen/splash.png
}

FILES:${PN} = "${datadir}/petasos-screen"
