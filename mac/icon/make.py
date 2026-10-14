#!/usr/bin/env python3
"""Pack rendered PNGs of petasos-icon.svg into ../Petasos.icns, which build.sh puts in the app.

    python3 make.py DIR     # DIR holds icon_16.png ... icon_1024.png rendered from the SVG

macOS's iconutil does the same from an .iconset; this needs nothing but Python, so the icon
can be remade anywhere."""
import struct
import sys
from pathlib import Path

# ICNS entry types and the rendered size each holds (the @2x ones reuse the larger renders).
ENTRIES = [("icp4", 16), ("icp5", 32), ("icp6", 64), ("ic07", 128), ("ic08", 256),
           ("ic09", 512), ("ic10", 1024), ("ic11", 32), ("ic12", 64), ("ic13", 256), ("ic14", 512)]

src = Path(sys.argv[1])
body = b""
for kind, size in ENTRIES:
    png = (src / f"icon_{size}.png").read_bytes()
    body += kind.encode() + struct.pack(">I", 8 + len(png)) + png
out = Path(__file__).resolve().parent.parent / "Petasos.icns"
out.write_bytes(b"icns" + struct.pack(">I", 8 + len(body)) + body)
print(f"wrote {out} ({out.stat().st_size} bytes)")
