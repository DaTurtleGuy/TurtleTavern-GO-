#!/usr/bin/env python3
"""Pack the frontend, default content and config into a zip the Android app extracts on first run.

Usage: python tools/pack_mobile_assets.py <gotavern_dir> <output_zip>
"""
import os
import sys
import zipfile


def main():
    src = os.path.abspath(sys.argv[1])
    out = sys.argv[2]
    os.makedirs(os.path.dirname(out), exist_ok=True)

    count = 0
    total = 0
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        cfg = os.path.join(src, "config.yaml")
        if os.path.exists(cfg):
            z.write(cfg, "config.yaml")
            count += 1
            total += os.path.getsize(cfg)

        for top in ("public", "default"):
            base = os.path.join(src, top)
            if not os.path.isdir(base):
                continue
            for root, dirs, files in os.walk(base):
                relroot = os.path.relpath(root, src).replace("\\", "/")
                if relroot.startswith("public/webpack/"):
                    dirs[:] = [d for d in dirs if d != "cache"]
                for name in files:
                    full = os.path.join(root, name)
                    rel = os.path.relpath(full, src).replace("\\", "/")
                    z.write(full, rel)
                    count += 1
                    total += os.path.getsize(full)

    print(f"{count} files, {total / 1048576:.1f} MB raw -> {os.path.getsize(out) / 1048576:.1f} MB zip")


if __name__ == "__main__":
    main()
