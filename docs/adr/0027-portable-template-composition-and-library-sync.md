# ADR-0027: Portable source composition and library synchronization

- **Status:** Accepted
- **Date:** 2026-09-18
- **Supersedes:** ADR-0025 (explicit-only source synchronization and installation-bound recipe selection)
- **Superseded by:** None

## Decision

Portable template exports contain source: lab declarations, playbooks, Dockerfiles, configuration and the pinned source dependency closure needed to recreate images. They never contain VM disks, OCI images, credentials, target identities, provider state or image approval. Internal build artifacts and provider image imports are separate from template import/export.

Versioned package metadata identifies logical builder requirements, source components and recipes. Publication resolves a bounded acyclic dependency graph to immutable content and vendors its source closure. Local administrator bindings satisfy logical builder requirements without changing source identity. Existing YAML, bundle and renderer semantics remain readable. New semantics select a new explicit version, never reinterpret prior revisions.

Source plugins optionally resolve collections. One sync pins one repository commit, validates the complete collection and publishes it atomically under current author authority. Stable source keys bind entries to local template identities. Unchanged content creates no duplicate revisions; removals mark upstream availability without deleting history or instances. Synced source is read-only; editing creates an independent copy.

Core persists project-scoped library configuration, sync history and fenced schedules. Default polling is fifteen minutes with backoff and no overlapping invocation per binding. Manual sync uses the same path. A schedule records an author and rechecks authority before publication; lost authority pauses it. The GitHub source plugin owns authenticated repository transport. Repository hooks, submodules and workflows never execute. Sync publishes source only; builds are explicit or required by launch preparation.

## Compatibility and validation

Preserve old wire fields, immutable revision identities, provider state and checksummed migrations. Use additive migrations and negotiated contracts. Test authorization, crash recovery and SQLite/PostgreSQL parity at changed boundaries. See [implementation contract](../design/portable-template-collaboration.md) and the dependency-ordered tasks in `design/checklist.yaml`.
