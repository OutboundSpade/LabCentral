# LabCentral agent guide

LabCentral is a Go control plane and plugin ecosystem for declarative, isolated virtual labs. Cybersecurity classrooms are the primary production use case; every guest, image, bundle, network, and third-party plugin is untrusted. The design-only Phase 0 gate is complete and the repository is implementation-ready; production application code has not started. Begin implementation only through the proposed tasks and dependency order in `design/checklist.yaml`, keeping the accepted contracts and ADRs authoritative.

## Mandatory reading order

1. `PROJECT_STATE.md`
2. `design/state.yaml`
3. `design/checklist.yaml`
4. Accepted ADRs relevant to the task under `docs/adr/`
5. The relevant files under `docs/design/`
6. `docs/references.md` when a claim depends on current provider behavior

Before changing anything, search these sources for contradictions and stable IDs. Preserve useful existing work.

## Design and implementation workflow

- Use stable IDs (`REQ-*`, `ADR-*`, `OQ-*`, `TASK-*`, `MILESTONE-*`); never silently renumber a referenced ID.
- Keep portable semantics in `internal/domain`; HTTP handlers, database adapters, and providers depend inward on domain/application ports.
- Huma/OpenAPI is the canonical product API. The static SvelteKit UI and future clients use only that API.
- Provider and access plugins are separate processes and communicate only through the versioned protocol. They never access the control-plane database or make authorization decisions.
- Treat provider capabilities as discovered, target-specific facts. Do not infer support merely from provider type.
- Treat container isolation as insufficient for arbitrary hostile code unless a separately verified sandbox or VM boundary is selected.
- Use typed structures and versioned schemas. Do not introduce generic portable `map[string]any` resources or executable templates.
- Record primary sources and retrieval dates in `docs/references.md` when provider behavior affects a decision.
- Add or update tests and contract fixtures with implementation changes.

## Changing accepted decisions

Never edit an accepted ADR to reverse its decision. Create a new ADR, link and mark the old ADR superseded, explain the reason and compatibility/security effects, then update every affected design document, `PROJECT_STATE.md`, `design/state.yaml`, and `design/checklist.yaml`. Breaking public schema or protocol changes require an ADR.

## Documentation consistency

The ADR is authoritative for a decision; schemas/protocol definitions are authoritative for machine contracts; design documents explain them; state files report progress. A change is incomplete if these disagree. Validate YAML, JSON, Proto, Mermaid, examples, links, and stable-ID references where tooling permits.

## End-of-session requirements

Before ending a design or implementation session:

1. Update affected design documents and ADRs.
2. Update `PROJECT_STATE.md`, `design/state.yaml`, and `design/checklist.yaml`.
3. Update `docs/design/open-questions.md` and source references as needed.
4. Record completed work and the next concrete action.
5. Confirm no accepted decision exists only in chat.
