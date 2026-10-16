## Downloads

| File | For |
| --- | --- |
| `labcentral-linux-amd64.tar.gz` | Operators: the `labcentral` CLI and its `labcentral-provider`, for running labs on AWS or Proxmox |
| `labcentral-windows-amd64.exe` | Validating lab templates on Windows |
| `labcentral-darwin-arm64`, `labcentral-darwin-amd64` | Validating lab templates on a Mac (Apple silicon or Intel) |

Check a download against `SHA256SUMS`, or verify that this repository's release
workflow built it:

```sh
gh attestation verify labcentral-windows-amd64.exe --repo OutboundSpade/LabCentral
```

Running labs also needs the pinned Packer toolchain; see
[Running labs with the CLI](https://github.com/OutboundSpade/LabCentral/blob/main/docs/operators.md#install).
Validating templates needs nothing else; see
[Writing lab templates](https://github.com/OutboundSpade/LabCentral/blob/main/docs/templates.md#validate-test-and-publish).
