# LabCentral agent guide

LabCentral is a Go control plane and plugin ecosystem for declarative, isolated virtual labs. Cybersecurity classrooms are the primary production use case; every guest, image, bundle, network, and third-party plugin is untrusted. Runtime services, SQLite/PostgreSQL persistence, Docker and Proxmox providers, and classroom governance/access are implemented; `PROJECT_STATE.md` and `design/state.yaml` report the current production-validation gaps. Continue implementation only through the proposed tasks and dependency order in `design/checklist.yaml`, keeping the accepted contracts and ADRs authoritative.

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
- Add or update tests and contract fixtures for changed behavior and meaningful regressions; avoid tests that merely mirror implementation.

## Validation proportional to the change

- During iteration, run the smallest checks that exercise the changed behavior. Reuse passing results while their code, dependencies, configuration and build inputs remain unchanged.
- Docs-only changes need relevant syntax/link/consistency checks, not builds or runtime tests. UI changes need type/build checks and affected browser scenarios; run the cross-browser suite once for a settled shared-navigation/layout change. Backend changes need affected package tests; concurrency, persistence and authorization changes also need relevant race and database-parity tests.
- Run broad integration checks once when the affected boundaries justify them. After a correction, rerun affected checks; repeat the broader suite only if the correction invalidates its earlier evidence. Preserve required CI/release gates.
- Validate representative plans locally before live provisioning, including multiple resources sharing an allocation namespace. Catch planning/admission errors before staging deployment artifacts.
- Settle and review implementation before the release build, upload and deployment. Reuse that exact artifact. After deployment, perform a focused health/feature smoke test and clean up only test-owned resources.
- Retain hashes for artifact identity, transfer/reconstruction integrity, signed provider qualification and ownership/image contracts. Compute release artifact digests once and verify them at deployment boundaries; remeasure if bytes change. Do not repeatedly hash unrelated unchanged files or duplicate digest inventories in documents. Hashes identify bytes; tests establish behavior and signatures establish provenance.
- Reuse unexpired provider qualification only while every bound fact still matches. Core/UI/docs changes alone do not require provider requalification. Changed provider identity, bound target facts or expired evidence still require the accepted qualification procedure; do not bypass runtime gates to save time.
- Keep host-specific testing/deployment details, endpoints, configuration, inventory, installed digests, qualification expiry and recovery records outside tracked source in protected operator records. Keep public documentation portable.
- Keep detailed logs and qualification evidence in ignored artifacts or CI. Summarize results and material limitations in the final response; do not create a repository narrative for every iteration.

## Changing accepted decisions

Never edit an accepted ADR to reverse its decision. Create a new ADR, link and mark the old ADR superseded, explain the reason and compatibility/security effects, then update every affected design document, `PROJECT_STATE.md`, `design/state.yaml`, and `design/checklist.yaml`. Breaking public schema or protocol changes require an ADR.

## Documentation consistency

The ADR is authoritative for a decision; schemas/protocol definitions are authoritative for machine contracts; design documents explain them; state files report progress. A change is incomplete if these disagree. Validate affected YAML, JSON, Proto, Mermaid, examples, links, and stable-ID references where tooling permits.

## End-of-session requirements

Before ending a design or implementation session:

1. Update user/developer documentation when behavior, setup or architecture changes; use an ADR only when the decision requires one.
2. Update `PROJECT_STATE.md`, `design/state.yaml`, and `design/checklist.yaml` only when their reported status, task scope, next action or gates change. Batch these updates at task completion, not after every revision or deployment retry.
3. Update open questions and source references only when their content changes. Do not append a historical entry merely because another session occurred.
4. Keep one concise current deployment record outside tracked source with the information needed to operate or roll back. Keep intermediate builds, transcripts and repeated hash lists out of repository documentation; preserve existing useful evidence.
5. Summarize completed work, relevant checks and remaining limitations in the final response. Confirm no accepted architectural decision exists only in chat.
