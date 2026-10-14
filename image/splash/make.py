#!/usr/bin/env python3
"""Turn rendered 800x480 PNGs into the image's two boot pictures.

  python make.py splash.png bootlogo.png

Writes petasos-bootup.bmp (u-boot's logo, from bootlogo.png: 480x800, turned a quarter clockwise,
32-bit BGRA bottom-up, as superbird-logo expects) and petasos-splash.png (weston's background,
from splash.png, landscape) into the layer, and copies the splash for the kiosk page. Render
petasos-bootlogo.svg (the winged hat) and petasos-splash.svg (the Greek key) at 800x480 first, in
any browser, with Caladea and Inter installed.
"""
import shutil
import struct
import sys
from pathlib import Path

from PIL import Image

LAYER = Path(__file__).resolve().parent.parent / "meta-petasos"

src = Image.open(sys.argv[1]).convert("RGB")
logo = Image.open(sys.argv[2]).convert("RGB")
for image in (src, logo):
    assert image.size == (800, 480), f"expected 800x480, got {image.size}"
png = LAYER / "recipes-graphics/weston-init/files/petasos-splash.png"
src.save(png, optimize=True)
shutil.copy(png, LAYER / "recipes-extended/petasos-screen/files/splash.png")

portrait = logo.rotate(-90, expand=True).convert("RGBA")
w, h = portrait.size
pixels = portrait.transpose(Image.Transpose.FLIP_TOP_BOTTOM).tobytes("raw", "BGRA")
# BITMAPV5HEADER with BI_BITFIELDS, the same layout as yocto-superbird's own boot logos.
info = struct.pack("<IiiHHIIiiII4I", 124, w, h, 1, 32, 3, len(pixels), 2835, 2835, 0, 0,
                   0x00FF0000, 0x0000FF00, 0x000000FF, 0xFF000000)
info += b"BGRs" + bytes(36) + struct.pack("<4I", 4, 0, 0, 0)
offset = 14 + len(info)
with open(LAYER / "recipes-bsp/superbird-logo/files/petasos-bootup.bmp", "wb") as f:
    f.write(b"BM" + struct.pack("<IHHI", offset + len(pixels), 0, 0, offset) + info + pixels)
