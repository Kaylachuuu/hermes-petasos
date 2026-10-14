# petasos-image lets root log in with an empty password. That's fine over the USB
# cable, but not on the phone's hotspot or the WireGuard tunnel, so SSH only accepts root from the
# USB link's 10.42.1.x network. IPv6 link-local isn't listed: logins from the Mac over it were
# refused even with fe80::/10 allowed (likely the interface suffix sshd sees), so use ssh -4.
do_install:append() {
    sed -i '1i AllowUsers root@10.42.1.0/24' ${D}${sysconfdir}/ssh/sshd_config
    grep -q '^AllowUsers root@10.42.1.0/24$' ${D}${sysconfdir}/ssh/sshd_config
}
