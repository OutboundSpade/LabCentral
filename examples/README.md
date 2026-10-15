# Portable labs

Every lab directory is complete shared source using `lab.yaml` version 3. Use the
same directory with either platform's environment file. Desktop machines' builds
install the platform's desktop transport.

- `single`: SSH to `server`, then run `curl http://localhost`.
- `web-database`: SSH to `web`, then run `pg_isready -h database` and
  `curl http://localhost`. Machine names resolve to private lab addresses. To
  create a database user, SSH to `database` and use `sudo -u postgres psql`.
- `workstation`: request desktop access and use the printed guest credentials.
  Set `welcome` through an inputs JSON file such as `{"welcome":"Hello, class"}`.
- `starter-template`: a complete hosted template to copy when writing your own: a
  web server and a desktop workstation, a student handover, answer/checked/instructor
  stages, two checks, two breaks with a reset, and a three-variant scenario. See
  [Writing lab templates](../docs/templates.md).

These examples request no general Internet egress at runtime. Change software in
its image playbook and rebuild; package installation belongs in build guests.
Operator configuration and credentials belong outside the shared source.

The examples are authoring fixtures, not evidence of live platform qualification.
The separate `consumer` Go module demonstrates using the library without the CLI.
See [the design](../ARCHITECTURE.md) for capabilities and qualification limits.
