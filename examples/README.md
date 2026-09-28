# Portable labs

Every directory is complete shared source. Use the same directory with either
platform's environment file. Only the workstation asks for a desktop; the
platform installs the appropriate desktop transport during the build.

- `single`: SSH to `server`, then `curl http://localhost`.
- `web-database`: SSH to `web`, then `pg_isready -h database` and
  `curl http://localhost`. Machine names resolve to private lab addresses. To
  create a database user, SSH to `database` and use `sudo -u postgres psql`.
- `workstation`: request desktop access and use the printed guest credentials.
  Set `welcome` with an inputs JSON file such as `{"welcome":"Hello, class"}`.

Runtime machines have no general Internet egress. Change software in the playbook
and rebuild; package installation belongs in the temporary build machine.

The examples are authoring fixtures, not evidence of live acceptance. The separate
`consumer` Go module demonstrates use without the CLI.
