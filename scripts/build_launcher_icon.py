#!/usr/bin/env python3
# Copyright 2026 The peersh Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Build peersh app icon assets from the launcher icon master.
#
# Source:
#   app/icon/ic_launcher_1024.png
#
# Outputs:
#   app/icon/google_play_icon_512.png
#   app/android/app/src/main/res/mipmap-*/ic_launcher.png
#   windows/assets/peersh.ico
#   windows/assets/peersh_256.png
#
# The Google Play icon is exported as a full-square 512x512 32-bit PNG.
# Google Play applies the rounded mask and external drop shadow dynamically,
# so those treatments should not be baked into the source artwork.

from pathlib import Path

from PIL import Image


REPO = Path(__file__).resolve().parent.parent
MASTER_PATH = REPO / "app" / "icon" / "ic_launcher_1024.png"
PLAY_ICON_PATH = REPO / "app" / "icon" / "google_play_icon_512.png"
RES_DIR = REPO / "app" / "android" / "app" / "src" / "main" / "res"
WINDOWS_ASSETS = REPO / "windows" / "assets"

ANDROID_DENSITIES = {
    "mdpi": 48,
    "hdpi": 72,
    "xhdpi": 96,
    "xxhdpi": 144,
    "xxxhdpi": 192,
}

ICO_SIZES = [
    (16, 16),
    (24, 24),
    (32, 32),
    (48, 48),
    (64, 64),
    (128, 128),
    (256, 256),
]


def load_master() -> Image.Image:
    if not MASTER_PATH.exists():
        raise FileNotFoundError(f"missing icon master: {MASTER_PATH}")

    img = Image.open(MASTER_PATH).convert("RGBA")
    opaque = Image.new("RGBA", img.size, (0, 0, 0, 255))
    opaque.alpha_composite(img)
    return opaque.resize((1024, 1024), Image.Resampling.LANCZOS)


def save_png(img: Image.Image, path: Path, size: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    img.resize((size, size), Image.Resampling.LANCZOS).save(path, format="PNG", optimize=True)
    print(f"wrote {path} ({size}x{size})")


def main() -> None:
    master = load_master()

    save_png(master, PLAY_ICON_PATH, 512)

    for density, px in ANDROID_DENSITIES.items():
        save_png(master, RES_DIR / f"mipmap-{density}" / "ic_launcher.png", px)

    WINDOWS_ASSETS.mkdir(parents=True, exist_ok=True)
    ico_path = WINDOWS_ASSETS / "peersh.ico"
    master.save(ico_path, format="ICO", sizes=ICO_SIZES)
    print(f"wrote {ico_path}")

    save_png(master, WINDOWS_ASSETS / "peersh_256.png", 256)


if __name__ == "__main__":
    main()
