# Debian networking pair

Requires the measured Debian classroom image and a qualified Proxmox target.
Each VM has one CPU and 512 MiB RAM. Open each browser console, then use
`ip address`, `ip route` and `ping` to explore the network.

Alice (10.220.0.10) and Bob (10.220.0.11) share one isolated LAN.
Ping the peer and inspect `ip neigh` to see address resolution.

Both networks deny external egress. End the lab to remove both VMs.
