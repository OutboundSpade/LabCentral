#!/usr/bin/env bash
set -euo pipefail
# Inputs are operator-selected, already verified artifacts. This never rebuilds them.
packer_binary=${PACKER_BINARY:?Set PACKER_BINARY to the verified Packer 1.14.3 executable}
plugin_directory=${PACKER_PLUGIN_PATH:?Set PACKER_PLUGIN_PATH to the verified plugin directory}
test -x bin/labcentral
test -x bin/labcentral-provider
packer_version=$("$packer_binary" version)
[[ "${packer_version%%$'\n'*}" == "Packer v1.14.3" ]]
python3 - "$plugin_directory" <<'PY'
import pathlib,sys
root=pathlib.Path(sys.argv[1]); expected={'amazon':'1.8.1','proxmox':'1.2.3','ansible':'1.1.4'}
for name,version in expected.items():
 entries=list(root.glob(f'github.com/hashicorp/{name}/packer-plugin-{name}_*_linux_amd64'))
 assert len(entries)==1 and entries[0].name==f'packer-plugin-{name}_v{version}_x5.0_linux_amd64',f'Unexpected {name} plugin binaries'
PY
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cp bin/labcentral bin/labcentral-provider "$stage/"
cp "$packer_binary" "$stage/packer"
cp "${PACKER_LICENSE:-$(dirname "$packer_binary")/LICENSE.txt}" "$stage/Packer-LICENSE.txt"
cp -r "${PLUGIN_LICENSE_DIRECTORY:?Set PLUGIN_LICENSE_DIRECTORY to the pinned plugin LICENSE files}" "$stage/plugin-licenses"
cp -a "$plugin_directory" "$stage/packer-plugins"
cp LICENSE tools/toolchain.json "$stage/"
mkdir -p "$stage/noVNC-license"
cp access/web/vendor/LICENSE.txt access/web/vendor/AUTHORS "$stage/noVNC-license/"
cp -r access/web/vendor/docs "$stage/noVNC-license/"
(cd "$stage" && find . -type f ! -name SHA256SUMS.tmp -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS.tmp && mv SHA256SUMS.tmp SHA256SUMS)
mkdir -p dist
tar -C "$stage" -czf dist/labcentral-v2-dev-linux-amd64.tar.gz .
(cd dist && sha256sum labcentral-v2-dev-linux-amd64.tar.gz > SHA256SUMS)
