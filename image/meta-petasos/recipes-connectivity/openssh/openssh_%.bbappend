# The image allows root with an empty password (yocto-superbird's base). That's fine over the USB
# cable, but not on the phone's hotspot or the WireGuard tunnel, so SSH only accepts root from the
# USB link: its 10.42.1.x network and IPv6 link-local addresses (how nocturne.local-style mDNS
# names usually resolve over USB).
do_install:append() {
    sed -i '1i AllowUsers root@10.42.1.0/24 root@fe80::/10' ${D}${sysconfdir}/ssh/sshd_config
    grep -q '^AllowUsers root@10.42.1.0/24 root@fe80::/10$' ${D}${sysconfdir}/ssh/sshd_config
}
