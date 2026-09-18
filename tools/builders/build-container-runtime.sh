#!/usr/bin/env bash
set -euo pipefail
if [[ $# != 3 ]]; then
  echo 'usage: build-container-runtime.sh VERIFIED_DEBIAN_BASE RUNTIME_AGENT NEW_OUTPUT_QCOW2' >&2
  exit 2
fi
runtime_base=$(realpath "$1")
runtime_agent=$(realpath "$2")
runtime_output=$(realpath -m "$3")
runtime_tools=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -f "$runtime_base" && -f "$runtime_agent" && ! -e "$runtime_output" ]]
python3 - "$runtime_tools/toolchain.lock.json" "$runtime_base" <<'PY'
import hashlib,json,sys
from pathlib import Path
expected=json.loads(Path(sys.argv[1]).read_text())['baseImage']['sha512']
with open(sys.argv[2],'rb') as source:
    assert hashlib.file_digest(source,'sha512').hexdigest()==expected, 'unrecognized Debian construction base'
PY
qemu-img create -f qcow2 "$runtime_output" 12G
virt-resize --expand /dev/sda1 "$runtime_base" "$runtime_output"
virt-customize -a "$runtime_output" --memsize 2048 --smp 2 \
  --mkdir /usr/libexec --upload "$runtime_agent:/usr/libexec/labcentral-container-vm" \
  --run "$runtime_tools/install-container-runtime.sh"
chmod 0600 "$runtime_output"
echo 'Fixed runtime base created. Pin its disk and agent hashes in both builder and import policy.'
