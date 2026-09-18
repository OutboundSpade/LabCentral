#!/bin/sh
# Run ONLY inside a disposable, trusted toolchain-image construction guest.
# /opt/labcentral/assets must come from fetch-toolchain.py's verified closure.
set -eu
export DEBIAN_FRONTEND=noninteractive
export PATH=/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
apt-get update
apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils python3 openssh-client libguestfs-tools linux-image-amd64 ca-certificates unzip iproute2
install -d -m 0755 /usr/local/bin /opt/labcentral/packer-plugins /opt/labcentral/offline-ansible/pip
python3 - <<'PY'
from pathlib import Path
import tarfile,zipfile
assets=Path('/opt/labcentral/assets')
with zipfile.ZipFile(assets/'packer.zip') as archive:
    Path('/usr/local/bin/packer').write_bytes(archive.read('packer'))
for package,kind in [('qemu-plugin.zip','qemu'),('ansible-plugin.zip','ansible')]:
    with zipfile.ZipFile(assets/package) as archive:
        name=next(n for n in archive.namelist() if n.startswith('packer-plugin-'+kind+'_') and not n.endswith('.txt'))
        dest=Path('/opt/labcentral')/name
        dest.write_bytes(archive.read(name));dest.chmod(0o755)
with tarfile.open(assets/'buildkit.tar.gz') as archive:
    for member in archive:
        if not member.isfile() or not member.name.startswith('bin/'):
            continue
        name=Path(member.name).name
        dest=Path('/usr/local/bin')/name
        dest.write_bytes(archive.extractfile(member).read());dest.chmod(0o755)
with zipfile.ZipFile(assets/'pip.whl') as archive:
    archive.extractall('/opt/labcentral/offline-ansible/pip')
PY
chmod 0755 /usr/local/bin/packer /usr/local/bin/lab-builder-vm
export PACKER_PLUGIN_PATH=/opt/labcentral/packer-plugins
for kind in qemu ansible; do
    for binary in /opt/labcentral/packer-plugin-"$kind"_*; do
        packer plugins install --path "$binary" "github.com/hashicorp/$kind"
        rm "$binary"
    done
done
mv /opt/labcentral/assets/wheels /opt/labcentral/offline-ansible/wheels
cp /opt/labcentral/ansible-requirements.lock /opt/labcentral/offline-ansible/requirements.txt
# The guest exposes no SSH listener or credential login. Its only management
# channel is the fixed virtio-serial agent, separate from the egress interface.
systemctl disable ssh.service ssh.socket 2>/dev/null || true
touch /etc/cloud/cloud-init.disabled
install -d -m 0755 /usr/local/libexec
cat > /usr/local/libexec/labcentral-grow-root <<'GROW'
#!/bin/sh
set -eu
root_device=$(findmnt -n -o SOURCE /)
case "$root_device" in /dev/vda[1-9]) ;; *) exit 1 ;; esac
partition=${root_device#/dev/vda}
growpart /dev/vda "$partition" || true
resize2fs "$root_device"
GROW
chmod 0755 /usr/local/libexec/labcentral-grow-root
cat > /etc/systemd/system/labcentral-build-agent.service <<'UNIT'
[Unit]
Description=LabCentral disposable build guest agent
After=systemd-udev-settle.service network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStartPre=/usr/local/libexec/labcentral-grow-root
ExecStart=/usr/local/bin/lab-builder-vm guest
Restart=always
RestartSec=1
StandardOutput=null
StandardError=journal
[Install]
WantedBy=multi-user.target
UNIT
systemctl enable labcentral-build-agent.service
cat > /etc/systemd/network/10-builder.network <<'NETWORK'
[Match]
Name=en* eth*
[Network]
DHCP=ipv4
IPv6AcceptRA=no
NETWORK
systemctl disable networking.service 2>/dev/null || true
systemctl enable systemd-networkd.service
# Save exact installed distribution versions in the pinned guest image.
dpkg-query -W > /opt/labcentral/debian-packages.lock
rm -rf /opt/labcentral/assets /var/lib/apt/lists/* /var/cache/apt/archives/* /var/lib/cloud/*
rm -f /etc/ssh/ssh_host_* /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys
truncate -s 0 /etc/machine-id
