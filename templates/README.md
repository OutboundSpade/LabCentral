# Standard template library

Each directory is a portable lab with a student README.md, `lab.yaml`, image
playbooks and private hosted `instructor.yaml` metadata. The Go library loads
portable source; hosted imports extract instructor material before building
images or providing student downloads.

`ccdc-service-recovery` is a Linux CCDC preparation lab with a workstation, a
services machine, three baseline stages, two independent service breaks, native
guest checks and three randomized combinations, including a combined outage.
Source presence and local validation do not establish live platform qualification.

To import a template, place its directory under `templates/` in your configured
template repository, commit the revision, then refresh and build through
Admin → Templates. Each import pins that commit; existing meetings and attempts
retain their revision. Stages stay with the template, without a publication editor
or manually duplicated teaching material.

Templates use instructor format v3. `visibility.audience` selects students or
coaches, and only coach revisions may hide machines. `practice.desktopMachine`
names a visible, desktop-enabled machine whenever stages or scenarios exist.
Optional `library` metadata supplies category, beginner/intermediate/advanced
difficulty, estimated minutes and up to twelve unique tags.

Stages own instructions, optional solutions and exactly one `completion`: an
`answer` with accepted alternatives and case policy, a pinned `check`, or
`instructor: true`. Baseline practice excludes break-linked stages and instructor
checkpoints. Keep accepted answers and private programs out of student handovers;
solutions become visible only through an explicit authorized reveal.

Randomized `scenarios` declare 2–32 distinct variants. Each selects 1–8 existing
`breaks` in execution order, self-paced `stages` and `compatible: true`. Every
selected break needs an action recovery stage. Qualify combinations by running the
shared reset followed by all selected breaks: break playbooks must not reset the
lab or undo another selected break. The server resets once and publishes readiness
only after confirmed application. An uncertain mutation requires confirmed cleanup
and a new attempt.

Guest steps always deliver their playbook. `files` adds private helpers, `assets`
selects portable files under `template/`, and generated identities/context use
`labcentral/`. Reserved paths and payload collisions fail validation. Visible
guests in hidden-machine templates require explicit image file and hostname-peer
selections excluding private topology and programs. Structural validation does
not inspect file contents; native image disclosure checks remain necessary.

Validate an author directory with `labcentral validate --template DIRECTORY`;
validation preserves private author files. Hosted import isolates directory errors,
reports them to administrators and saves each valid revision in one transaction.
See [the instructor contract](../ARCHITECTURE.md#instructor-format-v3-and-disclosure).

Keep environment configuration and provider credentials outside template source.
Qualify images, guest programs and combined breaks in an explicitly configured
test environment. Hosted release qualification requires real OIDC and rendered
desktop sessions; local fixtures cannot substitute for those checks.
