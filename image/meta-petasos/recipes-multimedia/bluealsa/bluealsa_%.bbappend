# The car's microphone: besides playing to the car (A2DP), BlueALSA answers the car's hands-free
# connection as a phone would (HFP/HSP audio gateway), so petasos can record from the car's
# microphone over it (bluealsa:PROFILE=sco) when the Device settings choose the car's.
# mSBC is the wideband (16 kHz) voice codec most cars offer; without it calls are 8 kHz.
SYSTEMD_BLUEALSA_ARGS = "-p a2dp-source -p a2dp-sink -p hfp-ag -p hsp-ag"
PACKAGECONFIG:append = " msbc"
