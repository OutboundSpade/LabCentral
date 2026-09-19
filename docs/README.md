# Documentation

Choose the guide for what you want to do. Product guides describe supported
workflows; design documents explain the contracts behind them.

| Audience | Start here | Contents |
| --- | --- | --- |
| Students, instructors and administrators | [Use LabCentral](users/README.md) | Sign in, launch labs, classroom batches, workspaces and operations |
| Deployment operators | [Operate LabCentral](operations/README.md) | Installation, authentication, plugins, monitoring, recovery and releases |
| Plugin authors | [Build a plugin](plugins/README.md) | Public SDK, manifest, protocol, conformance and installation |
| Contributors | [Develop LabCentral](development/README.md) | Build, tests, code map, API generation and design changes |
| Coding agents | [Agent instructions](../AGENTS.md) | Required reading, accepted contracts and task ordering |

## Reference and project records

- [Design reference](development/design-reference.md) groups the architecture and feature specifications.
- [ADRs](adr/) record accepted decisions and supersession; they are authoritative.
- [HTTP OpenAPI](../api/http/openapi.json), [plugin protocol](../api/plugin/v1/plugin.proto) and [Lab schema](../schemas/lab-v1alpha1.schema.json) define machine contracts.
- [Compatibility](compatibility.md) and [primary sources](references.md) document supported combinations and external evidence.
- [Current state](../PROJECT_STATE.md) summarizes what is complete and what comes next.
- [Task checklist](../design/checklist.yaml), [machine state](../design/state.yaml) and [verification evidence](../design/implementation-verification.yaml) track development.
- [History](history/) retains dated implementation evidence. Historical deployments and signatures are not current qualification.

Keep one canonical home for each subject. Link to contracts and runbooks instead
of copying their details into README files or progress summaries.

Portable sources and collaboration: [Template Library and feature packages](plugins/feature-packages.md), [project groups](users/project-groups.md), and [shared consoles](plugins/shared-console.md).
