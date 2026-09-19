# Template examples and requirements

| Example | Environment | Exercise |
| --- | --- | --- |
| [Debian starter](../../examples/classroom-debian/lab.yaml) | Qualified Proxmox | One Linux workstation |
| [Networking pair](../../examples/classroom-pair/README.md) | Qualified Proxmox | Two VMs, ping and neighbour discovery |
| [Network isolation](../../examples/classroom-isolation/README.md) | Qualified Proxmox | Two VMs on separate networks without routing |
| [Containers in dedicated VMs](../../examples/proxmox-containers/lab.yaml) | Qualified Proxmox container runtime | Web server and client, one VM each |
| [HTTP container](../../examples/docker-http/README.md) | Docker | Python web server with a published port |
| [SQLite web lab](../../examples/docker-laptop/lab.yaml) | Docker | Two containers and persistent database storage |

Docker examples require a Docker environment; they are not automatically translated
into VM-backed containers. For Proxmox, start with the
[container web/client example](../../examples/proxmox-containers/lab.yaml), which
explicitly requests VM isolation and the supported private-network runtime.
No separate runtime plugin is needed: the installed Proxmox provider owns this
implementation, and the target must have an approved, qualified runtime profile.
Catalogue publication validates the document; launch validation checks the chosen
environment's current capabilities, approved images and policy. Validation errors
identify the unsupported field or feature.

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

### Containers with VM isolation

For a qualified Proxmox runtime, declare `isolation: virtualMachine` and
`realizationPreference: [viaVM]` on each container. These fields express the
required isolation and realization; an image alone cannot imply that intent.
The [web/client example](../../examples/proxmox-containers/lab.yaml) uses one
VM per container and private static addresses. See [supported fields and console
commands](../operations/container-vm-runtime.md). Each container also consumes a
VM quota unit, with CPU and memory covering the entire guest.

The Template Library has separate Browse, Author, Import, Libraries, Images and
Activity views, according to your permissions. Choose the template and revision
once in Browse, then select the lab owner and environment to prepare it. Activity
retains preparation progress across reloads. Images lists declared recipes and the
current approval state of imported images; approval requires an image administrator.

The Author form edits single-file lab definitions. To change a portable template's
Dockerfiles, Ansible playbooks or other files, export its sources, edit locally,
repackage with `lab-template-pack`, and import the resulting source archive as an
independent template. No built images are included. Unsaved form/source edits survive
catalogue refresh; explicitly save or discard them before changing documents or
leaving. A GitHub library requires an operator-configured source binding; its
reference must match the configured branch, tag or commit (for example `main`).
