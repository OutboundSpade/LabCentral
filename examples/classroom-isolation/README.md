# Debian network isolation

Requires the measured Debian classroom image and a qualified Proxmox target.
Each VM has one CPU and 512 MiB RAM. Open each browser console, then use
`ip address`, `ip route` and `ping` to explore the network.

Alice (10.220.0.10) and Bob (10.221.0.10) are on separate networks.
There is no router or default route; cross-network traffic must fail.

Both networks deny external egress. End the lab to remove both VMs.
