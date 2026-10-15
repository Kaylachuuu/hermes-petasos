# The day the image was built, in /etc/os-release beside its version (DISTRO_VERSION), for the
# Device tab's Versions. DATETIME is left out of the task signature so it doesn't rebuild every time.
OS_RELEASE_FIELDS:append = " BUILD_ID"
BUILD_ID = "${@d.getVar('DATETIME')[:8]}"
BUILD_ID[vardepsexclude] = "DATETIME"
