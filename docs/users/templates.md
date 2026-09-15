# Template examples and requirements

| Example | Environment | Exercise |
| --- | --- | --- |
| [Debian starter](../../examples/classroom-debian/lab.yaml) | Qualified Proxmox | One Linux workstation |
| [Networking pair](../../examples/classroom-pair/README.md) | Qualified Proxmox | Two VMs, ping and neighbour discovery |
| [Network isolation](../../examples/classroom-isolation/README.md) | Qualified Proxmox | Two VMs on separate networks without routing |
| [HTTP container](../../examples/docker-http/README.md) | Docker | Python web server with a published port |
| [SQLite web lab](../../examples/docker-laptop/lab.yaml) | Docker | Two containers and persistent database storage |

The container examples are catalogue-only on PVE1. Choose a separately configured
Docker environment to run them. Catalogue publication validates the document;
it does not certify that the selected environment supports it. Launch validation
checks the environment’s current discovered capabilities and policy.

Catalogue names can contain spaces and Unicode (up to 63 characters). The
`metadata.name` inside Lab YAML still uses the declarative naming rules.

## Form and source editing

The template editor builds a form from the canonical Lab JSON Schema. Add nested
resources and optional fields, including VM compute, interfaces and container
settings. Source mode remains available for the complete YAML/JSON contract.
Form edits preserve document values but serialize JSON (valid YAML); comments
and YAML formatting are not retained. Unsupported schema combinations and freeform
maps use JSON fields. The form is an editor, and server validation remains authoritative.

Launch dialogs use declared template inputs and the selected provider’s advertised
override schema. **Customize options** overrides template defaults; **Use template
defaults** removes that override. A provider with no configurable options may
advertise an empty schema. Installation/service configuration still belongs to
the server operator.

## Do resources already describe the requirements?

Usually, yes: VM resources require VM support, and an isolated network requires
support for its network semantics. Providers validate those resource fields.
`requirements` is optional; authors do not need to repeat every resource type.
The renderer also derives resource dependencies from references, such as a VM’s
`networkRef`.

Explicit requirements add constraints. `workload.vm.native` asks for native VM
support, which is stronger than accepting an emulated implementation.
`isolationClass` expresses an isolation boundary; a container resource alone
cannot establish whether that boundary is a host process, sandbox or VM.
Keep explicit constraints when they communicate a classroom’s required boundary,
but avoid listing capabilities merely to restate its resources. The existing
Debian examples retain their explicit native VM boundary. No published revision
is silently weakened or rewritten.

Container process isolation is appropriate here only for trusted teaching
workloads. Arbitrary hostile code needs a separately verified sandbox or VM.
