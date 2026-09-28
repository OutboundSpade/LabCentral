# LabCentral v2

Read README.md and ARCHITECTURE.md. Keep the current design in ARCHITECTURE.md;
update it in place with implementation changes. No ADRs or duplicate status files.

This is a fresh Go library and CLI. Do not copy v1 implementation, tests, schemas,
protocols, documentation, or deployment machinery. Portable types depend on no
provider SDK. Plugins never open the library database. Author code runs only in
guests. Never claim live platform validation from mocks or local tests.

Keep source portable, credentials local, ownership explicit, and unknown outcomes
recoverable. Validate before provisioning. Never delete resources based only on
names or assume a timed-out mutation failed. Test meaningful failure cases and
run affected package/race checks; preserve passing evidence when inputs are unchanged.

Live checks require an explicitly configured test environment. Keep host-specific
configuration, credentials and evidence outside tracked source. Report incomplete
acceptance honestly. Do not deploy over existing installations.
