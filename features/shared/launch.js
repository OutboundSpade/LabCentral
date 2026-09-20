import { providerOptions } from "./editor.js";
import {
  el,
  field,
  check,
  options,
  all,
  request,
  mutate,
  button,
  message,
  poll,
  labCard,
  stableList,
} from "./ui.js";
export async function launchForm(
  parent,
  { owner, owners, onlaunch, batch = false, prepareMany } = {},
) {
  const form = el("form", "", {
    class: "grid gap-6 md:grid-cols-2 items-start",
  });
  parent.append(form);
  const settings = el("div", "", {
    class: "min-w-0 grid gap-x-4 sm:grid-cols-2",
  });
  form.append(settings);
  const template = field(settings, "Template", "select"),
    revision = field(settings, "Revision", "select"),
    target = field(settings, "Environment", "select");
  template.parentElement.classList.add("sm:col-span-2");
  const inputsBox = el("div", "", { class: "grid gap-x-4 md:grid-cols-2" });
  inputsBox.classList.add("sm:col-span-2");
  const minutes = field(settings, "Lab lifetime (minutes)", "input", {
    type: "number",
    min: 1,
    max: 527040,
    step: 1,
    placeholder: "Workspace default",
  });
  settings.append(inputsBox);
  const ack = check(
    form,
    "I understand Docker environments are for trusted code and do not isolate hostile workloads.",
  );
  ack.parentElement.classList.add("md:col-span-2");
  ack.parentElement.hidden = true;
  const progress = el("div", "", { role: "status", class: "md:col-span-2" });
  form.append(progress);
  let parameters = [],
    values = [],
    generation = 0,
    loadedRevision = "";
  let [templates, targets] = await Promise.all([
    all("templates.list"),
    request("targets.list"),
  ]);
  options(template, templates);
  options(target, targets.items ?? []);
  target.value = targets.items?.[0]?.id ?? "";
  const override = batch
    ? providerOptions(settings, () =>
        targets.items?.find((t) => t.id === target.value),
      )
    : null;
  const changeTarget = () => {
    const docker =
      targets.items?.find((t) => t.id === target.value)?.providerKind ===
      "docker";
    ack.parentElement.hidden = !docker;
    ack.checked = false;
  };
  target.onchange = () => {
    changeTarget();
    override?.reset();
    onlaunch?.({ targetId: target.value, changed: true });
  };
  changeTarget();
  function renderInputs() {
    inputsBox.replaceChildren();
    values = [];
    for (const p of parameters) {
      if (p.type === "secretRef") {
        inputsBox.append(
          el(
            "p",
            `${p.description || p.name}: administrator-managed secret reference required.`,
          ),
        );
        continue;
      }
      let control;
      if (p.type === "boolean" || p.type === "enum") {
        control = field(inputsBox, p.description || p.name, "select");
        options(
          control,
          (p.type === "boolean" ? ["true", "false"] : (p.enum ?? [])).map(
            (v) => ({ id: String(v), label: String(v) }),
          ),
          p.default === undefined
            ? "Choose an option"
            : `Default: ${p.default}`,
        );
      } else {
        control = field(
          inputsBox,
          p.description || p.name,
          p.type === "stringList" ? "textarea" : "input",
          {
            type: ["integer", "number"].includes(p.type) ? "number" : "text",
            min: p.min,
            max: p.max,
            step: p.type === "integer" ? 1 : "any",
            pattern: p.pattern,
            placeholder: p.default === undefined ? "" : String(p.default),
          },
        );
      }
      if (parameters.length === 1)
        control.parentElement.classList.add("md:col-span-2");
      control.required = p.required && p.default === undefined;
      values.push({ p, control });
    }
  }
  const readInputs = () =>
    values
      .filter((v) => v.control.value !== "")
      .map(({ p, control }) => {
        const raw = control.value,
          base = { name: p.name, kind: p.type };
        if (p.type === "boolean") return { ...base, boolean: raw === "true" };
        if (p.type === "enum") {
          const option = p.enum.find((v) => String(v) === raw);
          return typeof option === "number"
            ? { ...base, integer: option }
            : { ...base, string: raw };
        }
        if (p.type === "integer") return { ...base, integer: Number(raw) };
        if (p.type === "number") return { ...base, number: Number(raw) };
        if (p.type === "stringList")
          return { ...base, stringList: raw.split("\n") };
        return { ...base, string: raw };
      });
  async function revisionChanged() {
    const current = ++generation;
    loadedRevision = "";
    inputsBox.replaceChildren();
    parameters = [];
    values = [];
    if (!revision.value) return;
    const content = await request("templates.content", {
      templateId: template.value,
      revisionId: revision.value,
    });
    if (current !== generation) return;
    loadedRevision = revision.value;
    parameters = content.parameters ?? [];
    renderInputs();
  }
  const instructions = el("p", "", {
    class: "whitespace-pre-wrap leading-relaxed",
  });
  const lesson = el("aside", "", {
    class:
      "min-w-0 rounded-box border border-base-300 p-4 max-h-80 overflow-auto",
    "aria-label": "Template instructions",
  });
  lesson.append(el("h3", "Instructions"), instructions);
  settings.after(lesson);
  async function templateChanged() {
    instructions.textContent =
      templates.find((t) => t.id === template.value)?.description ||
      "No instructions provided.";
    const current = ++generation;
    options(revision, []);
    inputsBox.replaceChildren();
    if (!template.value) return;
    const revisions = await all("templates.revisions", {
      templateId: template.value,
    });
    if (current !== generation) return;
    options(
      revision,
      revisions
        .sort((a, b) => b.revisionNumber - a.revisionNumber)
        .map((r) => ({ id: r.id, label: `Revision ${r.revisionNumber}` })),
    );
    revision.value = revisions[0]?.id ?? "";
    await revisionChanged();
  }
  template.onchange = () =>
    templateChanged().catch((e) => message(e.message, true));
  revision.onchange = () =>
    revisionChanged().catch((e) => message(e.message, true));
  const submit = button(
    batch
      ? "Launch selected labs"
      : prepareMany
        ? "Deploy selected groups"
        : "Prepare and deploy",
    async () => {
      if (!form.reportValidity()) return;
      if (!revision.value || loadedRevision !== revision.value || !target.value)
        throw new Error("Select a published template and environment.");
      if (parameters.some((p) => p.type === "secretRef" && p.required))
        throw new Error(
          "This lab requires an administrator-managed secret reference.",
        );
      if (!ack.parentElement.hidden && !ack.checked)
        throw new Error("Acknowledge the container isolation requirement.");
      const inputs = readInputs();
      if (batch) {
        const selected = owners?.() ?? [];
        if (!selected.length) throw new Error("Select students first.");
        const result = await mutate("labs.batchCreate", {
          revisionId: revision.value,
          targetId: target.value,
          inputs,
          ...(minutes.value ? { ttlSeconds: Number(minutes.value) * 60 } : {}),
          acknowledged: ack.checked,
          owners: selected,
          ...(override?.read() !== undefined
            ? { providerOverride: override.read() }
            : {}),
        });
        progress.replaceChildren(el("p", "Classroom launch accepted."));
        await onlaunch?.(result);
        return;
      }
      if (prepareMany) {
        await prepareMany({
          revisionId: revision.value,
          targetId: target.value,
          inputs,
          lifetime: {
            ttlSeconds: minutes.value ? Number(minutes.value) * 60 : 0,
            persistent: false,
          },
          acknowledged: ack.checked,
        });
        return;
      }
      const selectedOwner = owner?.();
      if (owner && !selectedOwner)
        throw new Error("Join a group before deploying its lab.");
      const result = await mutate("labs.prepare", {
        revisionId: revision.value,
        targetId: target.value,
        inputs,
        lifetime: {
          ttlSeconds: minutes.value ? Number(minutes.value) * 60 : 0,
          persistent: false,
        },
        acknowledged: ack.checked,
        ...(selectedOwner ? { owner: selectedOwner } : {}),
      });
      const work = result.work ?? result;
      progress.replaceChildren(el("p", "Preparing your lab…"));
      watchPreparation(work.id, progress);
      await onlaunch?.(work);
    },
    "primary",
  );
  const actions = el("div", "", {
    class:
      "md:col-span-2 flex flex-wrap items-center gap-3 border-t border-base-300 pt-4",
  });
  actions.append(submit);
  form.append(actions);
  form.onsubmit = (e) => {
    e.preventDefault();
    submit.click();
  };
  return {
    actions,
    reload: async () => {
      const chosen = template.value;
      templates = await all("templates.list");
      options(template, templates);
      template.value = templates.some((t) => t.id === chosen) ? chosen : "";
      await templateChanged();
    },
    template,
    revision,
    target,
    readInputs,
    acknowledged: () => ack.checked,
    refresh: templateChanged,
    select: async (templateId, revisionId) => {
      template.value = templateId;
      await templateChanged();
      if (revisionId) {
        revision.value = revisionId;
        await revisionChanged();
      }
    },
  };
}
export function watchPreparation(id, parent) {
  let previous = "";
  let stop = () => {};
  const refresh = async () => {
    const w = await request("builds.get", { operationId: id });
    const signature = JSON.stringify(w);
    if (signature === previous) return;
    previous = signature;
    parent.replaceChildren(
      el(
        "p",
        w.result?.stage === "awaiting-approval"
          ? "Waiting for an administrator to approve the imported images."
          : `${w.result?.stage ?? w.state}${w.failureCode ? `: ${w.failureCode}` : ""}`,
      ),
    );
    if (w.result?.importIds?.length)
      parent.append(
        el("small", `Image approvals: ${w.result.importIds.join(", ")}`),
      );
    const deployed = w.result?.deployment;
    const instanceId = deployed?.instanceId ?? deployed?.InstanceID;
    if (instanceId)
      parent.append(
        button("Open lab", () => request("navigation.lab", { instanceId })),
      );
    if (["succeeded", "failed", "canceled"].includes(w.state)) {
      stop();
      return;
    }
    parent.append(
      button("Cancel preparation", async () => {
        await mutate("builds.cancel", { operationId: id });
        await refresh();
      }),
    );
  };
  stop = poll(refresh, 3000);
  void refresh().catch((e) => message(e.message, true));
}

export function preparations(
  parent,
  filter = () => true,
  { activeOnly = false } = {},
) {
  const panel = el("section"),
    list = el("div", "", { class: "space-y-3" });
  panel.append(
    el("h2", activeOnly ? "Preparing labs" : "Your recent preparations"),
    list,
  );
  list.append(el("p", "Loading preparations…", { role: "status" }));
  parent.append(panel);
  let pending = false;
  const refresh = async () => {
    if (pending) return;
    pending = true;
    try {
      const [work, labs, groups, templates] = await Promise.all([
        request("preparations.list"),
        all("labs.list"),
        all("groups.list", {}, "after"),
        all("templates.list"),
      ]);
      const rows = (work.items ?? [])
        .filter(filter)
        .filter((w) => !activeOnly || !w.result?.deployment)
        .map((w) => {
          const deployment = w.result?.deployment,
            id = deployment?.instanceId ?? deployment?.InstanceID;
          const lab = labs.find((l) => l.id === id),
            owner = w.request.preparation?.owner;
          const group = groups.find((g) => g.id === owner?.id);
          const accessible =
            lab &&
            lab.phase !== "destroyed" &&
            (!owner || owner.kind !== "team" || group?.joined);
          return {
            ...w,
            lab: accessible ? lab : null,
            label:
              lab?.name ||
              templates.find((t) => t.id === w.request.templateId)?.name ||
              "Lab preparation",
            ownerName:
              owner?.kind === "team"
                ? (group?.name ?? "Former team")
                : "Personal lab",
            wasDeployed: !!id,
          };
        });
      panel.hidden = activeOnly && !rows.length;
      stableList(list, rows, (w) => {
        const card = el("article"),
          stage =
            w.result?.stage === "awaiting-approval"
              ? "Waiting for image approval"
              : w.state === "queued"
                ? "Queued"
                : w.state === "running"
                  ? "Preparing image"
                  : w.state === "succeeded"
                    ? "Ready"
                    : (w.result?.stage ?? w.state);
        card.append(
          el("h3", w.label),
          el("p", `${w.ownerName} · ${stage}`),
          el(
            "small",
            w.createdAt ? new Date(w.createdAt).toLocaleString() : "",
          ),
        );
        if (w.failureCode)
          card.append(el("p", w.failureCode, { class: "error" }));
        if (!["succeeded", "failed", "canceled"].includes(w.state))
          card.append(
            button("Cancel preparation", async () => {
              await mutate("builds.cancel", { operationId: w.id });
              await refresh();
            }),
          );
        if (w.lab)
          card.append(
            button("Open lab", () =>
              request("navigation.lab", { instanceId: w.lab.id }),
            ),
          );
        else if (w.wasDeployed)
          card.append(
            el("p", "This lab has ended or is no longer accessible.", {
              class: "muted",
            }),
          );
        return card;
      });
      if (!rows.length)
        list.replaceChildren(el("p", "No recent preparations."));
    } finally {
      pending = false;
    }
  };
  poll(refresh);
  if (!activeOnly) void refresh().catch((e) => message(e.message, true));
  refresh.panel = panel;
  return refresh;
}
