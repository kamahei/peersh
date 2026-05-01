#!/usr/bin/env python3
# Copyright 2026 The peersh Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Build the Android launcher icon assets for peersh.
#
# Renders a single 1024x1024 master at app/icon/ic_launcher_1024.png and
# downscales it to the five Android density buckets under
# app/android/app/src/main/res/mipmap-*/ic_launcher.png.
#
# Design: a stylized prompt caret ">" inside a rounded square with a
# QUIC-orange-ish accent stroke, evoking "remote terminal, peer-to-peer."
# Deterministic — no randomness, no external files. Pillow only.

import os
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont, ImageFilter


REPO = Path(__file__).resolve().parent.parent
MASTER_DIR = REPO / "app" / "icon"
RES_DIR = REPO / "app" / "android" / "app" / "src" / "main" / "res"

DENSITIES = {
    "mdpi": 48,
    "hdpi": 72,
    "xhdpi": 96,
    "xxhdpi": 144,
    "xxxhdpi": 192,
}

MASTER = 1024
# Background gradient endpoints (deep navy -> midnight blue), evokes a
# terminal at night.
BG_TOP = (15, 23, 42)        # slate-900
BG_BOTTOM = (30, 41, 59)     # slate-800
ACCENT = (251, 146, 60)       # amber-400 — warm prompt accent
INK = (226, 232, 240)         # slate-200 — bright caret


def draw_master() -> Image.Image:
    size = MASTER
    img = Image.new("RGBA", (size, size), (0, 0, 0, 255))

    # Vertical gradient background.
    for y in range(size):
        t = y / (size - 1)
        r = int(BG_TOP[0] + (BG_BOTTOM[0] - BG_TOP[0]) * t)
        g = int(BG_TOP[1] + (BG_BOTTOM[1] - BG_TOP[1]) * t)
        b = int(BG_TOP[2] + (BG_BOTTOM[2] - BG_TOP[2]) * t)
        ImageDraw.Draw(img).line([(0, y), (size, y)], fill=(r, g, b, 255))

    draw = ImageDraw.Draw(img)

    # Soft amber halo behind the caret. Render to a separate layer so we
    # can blur it without smudging the prompt itself.
    halo = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    hd = ImageDraw.Draw(halo)
    cx, cy = size // 2, size // 2 + 20
    halo_r = size // 3
    hd.ellipse(
        [cx - halo_r, cy - halo_r, cx + halo_r, cy + halo_r],
        fill=(ACCENT[0], ACCENT[1], ACCENT[2], 110),
    )
    halo = halo.filter(ImageFilter.GaussianBlur(radius=size // 18))
    img.alpha_composite(halo)

    # (Underscore prompt cursor removed — the ">" caret alone reads as
    #  a shell prompt, and the underscore visually fought with the
    #  caret's lower arm at small sizes.)

    # Prompt caret ">" rendered as two thick strokes meeting at the
    # right-side apex. We use Pillow's draw.line with explicit width plus
    # solid endpoint circles for clean rounded caps. Two strokes never
    # self-intersect — the apex point is shared but visually merged.
    stroke_w = int(size * 0.13)
    arm_len = int(size * 0.36)
    apex_x = cx + arm_len // 2
    apex_y = cy
    top_x = cx - arm_len // 2
    top_y = cy - arm_len
    bot_x = cx - arm_len // 2
    bot_y = cy + arm_len

    cd = ImageDraw.Draw(img)
    # Upper arm: top-left tip -> apex
    cd.line(
        [(top_x, top_y), (apex_x, apex_y)],
        fill=INK,
        width=stroke_w,
    )
    # Lower arm: apex -> bottom-left tip
    cd.line(
        [(apex_x, apex_y), (bot_x, bot_y)],
        fill=INK,
        width=stroke_w,
    )
    # Round caps + apex blend.
    for px, py in [(top_x, top_y), (apex_x, apex_y), (bot_x, bot_y)]:
        cd.ellipse(
            [px - stroke_w // 2, py - stroke_w // 2, px + stroke_w // 2, py + stroke_w // 2],
            fill=INK,
        )

    # Top-right peer dot — a small accent marking "peer-to-peer".
    # Sits in the corner so it is still visible inside the round /
    # squircle adaptive-icon mask Android applies on top of our square.
    dot_r = size // 22
    dx = int(size * 0.74)
    dy = int(size * 0.26)
    draw.ellipse(
        [dx - dot_r, dy - dot_r, dx + dot_r, dy + dot_r],
        fill=ACCENT,
    )

    return img


def main() -> None:
    MASTER_DIR.mkdir(parents=True, exist_ok=True)
    master = draw_master()
    master_path = MASTER_DIR / "ic_launcher_1024.png"
    master.save(master_path, format="PNG", optimize=True)
    print(f"wrote {master_path} ({master.size[0]}x{master.size[1]})")

    for density, px in DENSITIES.items():
        outdir = RES_DIR / f"mipmap-{density}"
        outdir.mkdir(parents=True, exist_ok=True)
        out = outdir / "ic_launcher.png"
        scaled = master.resize((px, px), resample=Image.LANCZOS)
        scaled.save(out, format="PNG", optimize=True)
        print(f"wrote {out} ({px}x{px})")


if __name__ == "__main__":
    main()
