# LabCentral v2

Read README.md and ARCHITECTURE.md. Keep the current design in ARCHITECTURE.md; update
it in place with implementation changes. User guides live in `docs/` (templates,
operators, hosted, development); keep them in sync with behavior. No ADRs or duplicate
status files. Use Mermaid for diagrams.

The Go library and CLI remain independent of hosted identity and HTTP policy. The hosted
service lives in `hosted`; its UI is static SvelteKit under `web`. Do not copy v1
implementation, tests, schemas, protocols, documentation, or deployment machinery.
Portable types depend on no provider SDK. Plugins never open the library database.
Author code runs only in guests. Never claim live platform validation from mocks or
local tests.

Keep source portable, credentials local, ownership explicit, and unknown outcomes
recoverable. Validate before provisioning. Never delete resources based only on names or
assume a timed-out mutation failed. Test meaningful failure cases and run affected
package/race checks; preserve passing evidence when inputs are unchanged.

Live checks require an explicitly configured test environment. Keep host-specific
configuration, credentials and evidence outside tracked source. Report incomplete
acceptance honestly. Do not deploy over existing installations.

Hosted changes need affected authorization, persistence and race tests. UI changes need
type/build checks and browser inspection at desktop and mobile widths; use the daisyUI
Blueprint workflow for meaningful UI work. Keep generated public API types synchronized
with `go run ./tools/webapi`. Keep template revisions pinned, never expose
infrastructure credentials in APIs, and retain cleanup intent until provider
confirmation. Release acceptance requires real OIDC and desktop sessions.
