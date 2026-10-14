# Standard template library

Each directory is a self-contained, portable lab with a student README,
`lab.yaml`, image playbooks, and private hosted `instructor.yaml` metadata.
The Go library loads the portable source; hosted imports extract instructor
material before building or providing student downloads.

`ccdc-service-recovery` is a small Linux CCDC preparation lab with a workstation,
a branch services machine, three baseline stages, two independent service breaks,
native guest checks, and three randomized combinations including a combined
outage. It was imported into the hosted library and both native AWS images were
built on 2026-10-13. Real OIDC and rendered student desktop acceptance confirmed
the combined outage, both repairs and native checks, followed by a fresh web-only
attempt with independent progress. Owned test allocations were cleaned up.
The subsequent DCV startup fix passed a real student's first cold desktop launch
on an untouched AWS workstation. Larger cohorts and Proxmox remain unqualified.

To publish this template through the existing hosted importer, place its directory
under `templates/` in the configured private template repository, commit that
revision, then refresh and build it through Admin → Templates. No publication
editor or manually duplicated stages are required. Templates remain pinned to
that imported commit; existing meetings and attempts keep their revision.

Templates use version 3 instructor manifests with `library` metadata:
category, beginner/intermediate/advanced difficulty, estimated minutes and up to
twelve unique tags. Stages own instructions, accepted answers or check references,
and optional solutions. Baseline stages have no linked break.

Randomized `scenarios` declare 2–32 distinct variants. Each variant identifies
1–8 existing `breaks` in execution order, its self-paced `stages` and an explicit
`compatible: true` assertion. Every break must have an action recovery stage.
Certify combinations by testing the shared reset followed by all selected breaks:
break playbooks must not reset the lab or undo another selected break. The server
runs the reset once, applies the saved combination, and makes the desktop ready
only after confirmed completion. It will not generate arbitrary guest code or
reapply an uncertain mutation. A failed or interrupted application requires
confirmed cleanup and a new attempt.

Keep provider credentials and environment configuration outside template source.
Qualify new images, breaks, combined cases, real OIDC and rendered desktop sessions
in an explicitly configured test environment before claiming live acceptance.

Visibility is required. `practice.desktopMachine` names a visible, desktop-enabled
machine whenever stages or scenarios exist. Stages use exactly one `completion`:
`answer` with accepted alternatives and case policy, `check` or `instructor: true`.
Guest steps always deliver their playbook; `files` adds private helpers and `assets`
selects portable files under `template/`. Generated identities and extension context
use `labcentral/`. Reserved paths and payload collisions are rejected. A coach
revision’s visible guests cannot receive hidden identities or private image files.
Validate each author directory with `labcentral validate --template DIRECTORY`;
validation preserves the author’s private files. Hosted import isolates directory
errors, exposes them to administrators, and stores each revision in one transaction.
