# Portable source packages

Portable templates carry the source needed to reconstruct a lab: declarations,
playbooks, Dockerfiles, configuration and vendored dependencies. Image construction,
image transfer to a provider and image approval are separate operations. Export
never reads the image artifact store or the provider. Existing single-file YAML
and `labcentral.bundle/v1alpha1` revisions keep their existing semantics.

A `labcentral.bundle/v1beta1` archive contains `bundle.yaml` and exactly its indexed
source files. `package.apiVersion` is `labcentral.source-package/v1`. Every indexed
file has a SHA-256 digest, byte count and media type. Dependency roots have locks
over their relative file indexes; `requires` identifies other vendored roots in
the closure. Cycles, missing sources, overlapping roots and duplicate names fail
publication. Component imports contain one ordinary component declaration and use
an explicit local alias. Recipe imports contain one closed JSON build recipe;
their context paths are relative to that recipe's directory. Imported declarations
are resolved without changing the original archive or manifest.

Use `examples/template-library/composed-network/` as a source-package example:

```sh
go run ./cmd/lab-template lock --root examples/template-library/composed-network
go run ./cmd/lab-template pack --root examples/template-library/composed-network --output /tmp/network.lab.tar.gz
go run ./cmd/lab-template inspect --archive /tmp/network.lab.tar.gz
```

`lock` explicitly updates dependency and file digests after an intentional source
change. `pack` verifies dependency locks and creates deterministic archive bytes;
it refuses to overwrite an existing output file. Only explicitly listed files are
read. Source paths cannot escape the root or traverse symbolic links. This version
supports UTF-8 text sources, up to 127 source files and an 8 MiB archive/expanded
archive limit, including packaging overhead. Binary assets, nested archives, VM
disks and OCI layouts are rejected. Authors must keep credentials out of source,
including scripts and declared inputs; packaging does not identify arbitrary
secrets embedded in text.

Recipes use logical builder requirements, for example:

```yaml
package:
  apiVersion: labcentral.source-package/v1
  builders:
    - {id: linux, kind: ansible, schema: ansible-v1}
```

A builder installation binding sets `requirement: linux` for its authorized
projects. A destination may use different installation and binding IDs. Core
matches the logical ID, builder kind and recipe schema within the project; it
rejects ambiguous project mappings. Legacy recipes still select their original
physical installation/binding IDs. A legacy bundle containing build recipes must
be republished using logical requirements before portable export.

`POST /api/v1/template-import-previews` accepts `{projectId, archive}` with the
archive encoded as base64. It validates source/composition and reports dependencies,
declared capabilities and unresolved builder requirements without running a build
or persisting a revision. Create a template, then post the archive to its existing
`bundle-revisions` endpoint to publish. `GET /api/v1/lab-templates/{id}/revisions/{revisionId}/export?projectId=...`
returns `{archive, filename}`. Portable revision exports retain their exact bytes;
legacy YAML is wrapped as source. Target bindings and approvals are destination
configuration and are never attached to the export.

The feature bridge exposes `templates.importPreview` and `templates.export` under
`templates.write` for both operations (ADR-0031). Full source export requires
project template publication authority; participant template reads and launches
remain available. Core repeats authorization
independently of the declared browser permission.

Image recipes and dependency graphs are described in [disposable VM builders](vm-builders.md).
