# Standard template library

Ready-made hosted templates. Each directory is a complete template: `lab.yaml`, image
playbooks, a student `README.md`, and private `instructor.yaml` material. To write your
own, follow [Writing lab templates](../docs/templates.md) and start from
[examples/starter-template](../examples/starter-template/).

| Template | What students do |
| --- | --- |
| [`ccdc-service-recovery`](ccdc-service-recovery/README.md) | Restore a Linux branch website and scheduled operations: two machines, three baseline stages, two independent breaks, native checks and a three-variant outage scenario |

To use a template, copy its directory under `templates/` in your configured template
repository, commit, then refresh and build it in **Admin → Templates**.

Validating a template locally does not prove it works on a platform. Test its images,
breaks and checks in a real test environment before using it with a class.
