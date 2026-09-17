# Independent lifecycle and import fixture

This separate provider module uses only the published SDK/protocol. Configure ID `org.example.lifecycle`, kind `fixture`, protocol 1.0–1.1 and a protected `--state` journal. It simulates owned resources and inert image imports in that journal, preserving original invocation results across restart. It implements planning, apply/read/destroy, endpoint discovery, image import recovery and exact binding validation. It does not provision a real VM, confer production isolation or qualify any target. See [author examples](../../docs/plugins/extensions.md).
