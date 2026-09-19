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
} from "./ui.js";
export async function launchForm(
  parent,
  { owner, owners, onlaunch, batch = false } = {},
) {
  const form = el("form");
  parent.append(form);
  const template = field(form, "Template", "select"),
    revision = field(form, "Revision", "select"),
    target = field(form, "Environment", "select");
  const inputsBox = el("div");
  form.append(inputsBox);
  const minutes = field(form, "Lab lifetime (minutes)", "input", {
    type: "number",
    min: 1,
    max: 527040,
    step: 1,
    placeholder: "Workspace default",
  });
  const ack = check(
    form,
    "I understand Docker environments are for trusted code and do not isolate hostile workloads.",
  );
  ack.parentElement.hidden = true;
  const progress = el("div", "", { role: "status" });
  form.append(progress);
  let parameters = [],
    values = [],
    generation = 0,
    loadedRevision = "";
  const [templates, targets] = await Promise.all([
    all("templates.list"),
    request("targets.list"),
  ]);
  options(template, templates);
  options(target, targets.items ?? []);
  target.value = targets.items?.[0]?.id ?? "";
  const override = batch
    ? providerOptions(form, () =>
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
  async function templateChanged() {
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
    batch ? "Launch selected labs" : "Prepare and deploy",
    async () => {
      if (
        !form.reportValidity() ||
        !revision.value ||
        loadedRevision !== revision.value ||
        !target.value
      )
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
      progress.replaceChildren(el("p", `Preparation accepted: ${work.id}`));
      watchPreparation(work.id, progress);
      await onlaunch?.(work);
    },
    "primary",
  );
  form.append(submit);
  form.onsubmit = (e) => {
    e.preventDefault();
    submit.click();
  };
  return {
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
  let stop = () => {};
  const refresh = async () => {
    const w = await request("builds.get", { operationId: id });
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

export function preparations(parent, filter = () => true) {
  const panel = el("section");
  panel.append(el("h2", "Your recent preparations"));
  const list = el("div");
  panel.append(list);
  parent.append(panel);
  let pending = false;
  const refresh = async () => {
    if (pending) return;
    pending = true;
    try {
      const work = (await request("preparations.list")).items ?? [];
      list.replaceChildren();
      for (const w of work.filter(filter)) {
        const card = el("article");
        card.append(
          el(
            "p",
            `${w.request.preparation?.owner?.kind ?? "user"} ${w.request.preparation?.owner?.id ?? ""} · ${w.result?.stage === "awaiting-approval" ? "Waiting for image approval" : (w.result?.stage ?? w.state)}`,
          ),
          el("small", w.id),
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
        const deployment = w.result?.deployment;
        const id = deployment?.instanceId ?? deployment?.InstanceID;
        if (id)
          card.append(
            button("Open lab", () =>
              request("navigation.lab", { instanceId: id }),
            ),
          );
        list.append(card);
      }
      if (!list.childElementCount)
        list.append(el("p", "No recent preparations."));
    } finally {
      pending = false;
    }
  };
  poll(refresh);
  void refresh().catch((e) => message(e.message, true));
  return refresh;
}
