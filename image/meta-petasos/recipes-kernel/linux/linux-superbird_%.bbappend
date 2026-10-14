FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# Merged after the BSP's own fragments (see linux-superbird do_configure).
SRC_URI:append = " file://petasos.cfg"
