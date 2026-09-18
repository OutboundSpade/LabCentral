#!/usr/bin/env bash
# Build an operator-owned toolchain artifact, never a portable source package.
set -euo pipefail
if [[ $# != 3 && $# != 4 ]]; then
  echo 'usage: build-toolchain.sh VERIFIED_DEBIAN_BASE BUILDER_BINARY NEW_OUTPUT_QCOW2 [PINNED_CONTAINER_RUNTIME_QCOW2]' >&2
  exit 2
fi
builder_base=$(realpath "$1")
builder_binary=$(realpath "$2")
builder_output=$(realpath -m "$3")
builder_tools=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ ! -e "$builder_output" && -f "$builder_base" && -f "$builder_binary" ]]
builder_work=$(mktemp -d)
trap 'rm -rf -- "$builder_work"' EXIT
# This construction image has a fixed, reviewed partition layout.
python3 - "$builder_tools/toolchain.lock.json" "$builder_base" <<'PY'
import hashlib,json,sys
from pathlib import Path
expected=json.loads(Path(sys.argv[1]).read_text())['baseImage']['sha512']
with open(sys.argv[2],'rb') as source:
    assert hashlib.file_digest(source,'sha512').hexdigest()==expected, 'base image does not match the reviewed Debian lock'
PY
python3 "$builder_tools/fetch-toolchain.py" "$builder_work/assets"
qemu-img create -f qcow2 "$builder_output" 24G
virt-resize --expand /dev/sda1 "$builder_base" "$builder_output"
virt-customize -a "$builder_output" --memsize 4096 --smp 4 \
  --mkdir /opt/labcentral \
  --copy-in "$builder_work/assets:/opt/labcentral" \
  --upload "$builder_binary:/usr/local/bin/lab-builder-vm" \
  --upload "$builder_tools/ansible-requirements.lock:/opt/labcentral/ansible-requirements.lock" \
  --run "$builder_tools/install-toolchain.sh"
if [[ $# == 4 ]]; then
  builder_runtime=$(realpath "$4")
  [[ -f "$builder_runtime" ]]
  virt-customize -a "$builder_output" --memsize 2048 --smp 2 \
    --upload "$builder_runtime:/opt/labcentral/container-runtime.qcow2" \
    --chmod 0600:/opt/labcentral/container-runtime.qcow2
fi
chmod 0600 "$builder_output"
echo 'Toolchain image created. Pin its SHA-256 in the protected worker profile before use.'
