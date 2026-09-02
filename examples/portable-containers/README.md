# Example C — portable logical-container lab

Three digest-pinned OCI workloads communicate on one private L3 segment. The declaration requests process isolation and permits native, sandboxed, then via-VM realization; production policy may override this to require `virtualMachine`, producing a different capability requirement and plan.

| Target | Expected realization | Warning/difference |
|---|---|---|
| Docker | native Engine containers | Process isolation; Desktop networking differs; exec access. |
| Fly | native Machine per workload/app mapping on custom 6PN | IPv6 L3/DNS mapping; no arbitrary NIC/L2; lifecycle/port semantics differ. |
| Kubernetes | native Pods; configured RuntimeClass when sandbox required | Restart is Pod recreation; NetworkPolicy enforcement must be discovered. |
| Proxmox | one managed VM per logical container initially via shared runtime | Emulated container, VM boot/resource overhead; SSH/runtime shell; snapshot semantics differ. |
| AWS | EC2 VM per logical container initially via shared runtime | Emulated, cost/boot overhead; VPC/subnet/SG; reset by recreate. |
| KubeVirt with VM policy | VM per logical container via shared runtime | Emulated, CDI/CSI/network component dependencies. |

Graph: `network.service → {container.api, container.loadgen[one], container.loadgen[two], container.collector}` and `container.api → access.api-shell`. The typed `loadgen_ids` input demonstrates deterministic `forEach`: sorted stable keys become logical-resource identity suffixes. Parallel create is safe after the network. Example plan for Proxmox: `network.service` → approved bridge/VLAN mapping; each container → QEMU VM + boot disk + Ignition/bootstrap + OCI pull; warning `PORTABILITY_EMULATED_CONTAINER`; estimated 4 VMs/3.25 vCPU/896 MiB plus base-VM overhead; access chooses SSH because container exec is not native. Fly plan rejects the declared `containerExec` method unless an installed access plugin can faithfully proxy an authorized Machine shell.

Reset for Docker/Kubernetes/Fly normally recreates the workload and retains only declared persistent volumes; via-VM may recreate the VM or restore a compatible snapshot. No plan may call these identical, and policy requiring VM isolation makes Docker/Fly/Kubernetes-native targets ineligible.
