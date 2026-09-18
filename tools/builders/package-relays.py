#!/usr/bin/env python3
"""Package an already-built worker as independently installable builder relays."""
import argparse
import hashlib
import json
import os
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("binary", type=Path)
parser.add_argument("destination", type=Path)
args = parser.parse_args()
os.umask(0o022)
assert args.binary.is_file() and args.binary.stat().st_size <= 256 << 20
binary = args.binary.read_bytes()
digest = hashlib.sha256(binary).hexdigest()
schema = json.loads((Path(__file__).resolve().parents[2] / "schemas/vm-builder-binding-v1.schema.json").read_text())
args.destination.mkdir(mode=0o755, parents=True, exist_ok=False)

def encode(value):
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()

for kind in ("ansible", "dockerfile", "container-runtime"):
    root = args.destination / kind
    root.mkdir(mode=0o755)
    executable = root / "lab-builder-vm"
    executable.write_bytes(binary)
    executable.chmod(0o755)
    plugin_id = "io.labcentral.builder." + kind
    manifest = dict(apiVersion="labcentral.io/plugin-manifest/v2", id=plugin_id,
                    version="0.1.0", executable=executable.name, sha256=digest,
                    categories=["builder"], builderKinds=[kind], protocolMajor=1,
                    minimumProtocolMinor=0, maximumProtocolMinor=0,
                    operatingSystems=["linux"], architectures=["amd64"],
                    requestedPermissions=["isolated-builder-endpoint", "worker-tls-identity-read"],
                    configurationSchema=schema)
    contents = encode(manifest)
    (root / "plugin.json").write_bytes(contents)
    (root / "package.json").write_bytes(encode(dict(
        apiVersion="labcentral.io/extension-package/v1", id=plugin_id, version="0.1.0",
        contributions=[dict(id="builder", manifest="plugin.json", sha256=hashlib.sha256(contents).hexdigest())])))
    (root / "README.txt").write_text(
        "Install contribution 'builder' with explicit project bindings and granted permissions.\n"
        f"Installation arguments must include --kind {kind} and the worker endpoint, TLS CA,\n"
        "client certificate/key and expected TLS server name. Keep those local settings\n"
        "outside this package. Obtain the binding hashes with inspect-profile on the worker.\n")
print("Created independent Ansible, Dockerfile and fixed runtime packages; no worker endpoint or credentials included.")
