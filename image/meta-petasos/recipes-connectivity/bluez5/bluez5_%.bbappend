# The Car Thing tells phones it is a loudspeaker (Class of Device: Audio service, Audio/Video,
# Loudspeaker), so an iPhone offers it as somewhere to play music. BlueZ's default is
# "uncategorized". petasos also sets this at start (client/btclass_linux.go), for older images.
do_install:append() {
    if [ -f ${D}${sysconfdir}/bluetooth/main.conf ]; then
        sed -i 's/^#\?Class = .*/Class = 0x200414/' ${D}${sysconfdir}/bluetooth/main.conf
        grep -q '^Class = 0x200414' ${D}${sysconfdir}/bluetooth/main.conf || \
            sed -i '/^\[General\]/a Class = 0x200414' ${D}${sysconfdir}/bluetooth/main.conf
    fi
}
