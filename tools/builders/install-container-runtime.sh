#!/bin/sh
# Run only inside the trusted, pinned Debian construction guest.
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends podman uidmap fuse-overlayfs slirp4netns dbus-user-session qemu-guest-agent cloud-init openssh-server python3
useradd --uid 1001 --user-group --create-home --shell /usr/sbin/nologin labworkload
usermod --add-subuids 200000-265535 --add-subgids 200000-265535 labworkload
install -d -m 0755 /var/lib/systemd/linger
: > /var/lib/systemd/linger/labworkload
install -d -m 0700 -o labworkload -g labworkload /home/labworkload/.config /home/labworkload/.config/containers
cat > /home/labworkload/.config/containers/storage.conf <<'CONFIG'
[storage]
driver = "overlay"
[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
CONFIG
chown labworkload:labworkload /home/labworkload/.config/containers/storage.conf
chmod 0600 /home/labworkload/.config/containers/storage.conf
chmod 0755 /usr/libexec/labcentral-container-vm
cat > /etc/systemd/system/labcentral-container.service <<'UNIT'
[Unit]
Description=LabCentral dedicated container workload
Requires=user@1001.service
After=user@1001.service network-online.target
[Service]
Type=simple
ExecStart=/usr/libexec/labcentral-container-vm run
Restart=on-failure
RestartSec=3
[Install]
WantedBy=multi-user.target
UNIT
# Core starts the workload through the fixed agent after binding the guest.
systemctl enable qemu-guest-agent.service
apt-get clean
rm -rf /var/lib/apt/lists/* /var/lib/cloud /tmp/* /var/tmp/*
rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id
truncate -s 0 /etc/machine-id
