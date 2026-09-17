# Independent browser contribution fixture

This package imports only the published browser SDK. It displays authorized
context and probes the opaque-origin sandbox and denied bridge operations.
It is a fixture, not a topology diagram or a production terminal renderer.

Build the core/UI first, then run the proof (from the repository root):

```sh
cd web
bun run check
bun run build
cd ..
go build -trimpath -o bin/extension-foundations/labd ./cmd/labd
node web/tests/extensions.mjs
```

The proof packages these assets after the core build, starts that exact binary
with a UI-only installation and temporary local database, and checks Chromium,
Firefox and WebKit. It checks that the binary containing the bundled UI did not
change during installation. Test-owned services/data are cleaned up; logs go to
ignored `bin/extension-foundations/browser-proof.log`.

To produce a package independently:

```sh
node examples/external-browser/package.mjs /absolute/protected/package-directory
```

Installation is explicit and restart-based. This proof covers the browser
boundary only. It does not complete TASK-EXT-008 or qualify a verified release.
