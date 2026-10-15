# Examples

Every directory here is complete lab source using format version 3.

| Example | What it shows |
| --- | --- |
| [`single`](single/) | One SSH server. SSH to `server`, then run `curl http://localhost`. |
| [`web-database`](web-database/) | A web server and a PostgreSQL database. SSH to `web`, then run `pg_isready -h database` and `curl http://localhost`; machine names resolve to private lab addresses. |
| [`workstation`](workstation/) | A desktop machine. Request desktop access and use the printed credentials; set `welcome` through an inputs file such as `{"welcome":"Hello, class"}`. |
| [`starter-template`](starter-template/) | A complete hosted template to copy: a web server and a desktop, a student handover, stages, checks, breaks with a reset, and a randomized scenario. |
| [`windows-iis`](windows-iis/README.md) | Windows Server 2022 and a Linux client on AWS, with PowerShell builds and checks. |
| [`consumer`](consumer/main.go) | A separate Go module that drives the library without the CLI. |

The first three work on both AWS and Proxmox with the same source; `windows-iis` is
AWS-only. None of them need Internet access at runtime: software is installed by the
image build, so change it in the build playbook or script and rebuild. Keep
environment files and credentials outside lab source.

See [Writing lab templates](../docs/templates.md) for the format, and
[Qualification status](../ARCHITECTURE.md#qualification-status) for what has been
verified on real platforms.
