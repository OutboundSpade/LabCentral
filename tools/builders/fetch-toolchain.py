#!/usr/bin/env python3
"""Fetch reviewed toolchain assets; never execute or extract them on this host."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("destination", type=Path)
args = parser.parse_args()
lock = json.loads(Path(__file__).with_name("toolchain.lock.json").read_text())
assert lock["version"] == 1
args.destination.mkdir(parents=True, exist_ok=True)
for asset in lock["assets"]:
    assert not Path(asset["name"]).is_absolute() and ".." not in Path(asset["name"]).parts and asset["url"].startswith("https://")
    output = args.destination / asset["name"]
    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        with output.open("rb") as source:
            assert hashlib.file_digest(source, "sha256").hexdigest() == asset["sha256"], "cached toolchain asset changed"
        continue
    temporary = output.with_suffix(output.suffix + ".next")
    digest = hashlib.sha256()
    count = 0
    try:
        request = urllib.request.Request(asset["url"], headers={"User-Agent": "LabCentral-toolchain/1"})
        with urllib.request.urlopen(request, timeout=60) as source, temporary.open("xb") as target:
            while chunk := source.read(1 << 20):
                count += len(chunk)
                assert count <= 2 << 30, "toolchain asset exceeds bound"
                digest.update(chunk)
                target.write(chunk)
            target.flush()
            os.fsync(target.fileno())
        assert digest.hexdigest() == asset["sha256"], "toolchain digest mismatch"
        temporary.rename(output)
    finally:
        temporary.unlink(missing_ok=True)
    print("Verified", asset["name"])
