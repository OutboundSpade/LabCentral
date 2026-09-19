import {
  app,
  el,
  section,
  details,
  check,
  button,
  request,
  mutate,
  all,
  can,
  message,
  poll,
  confirm,
  labCard,
} from "./ui.js";
import { launchForm } from "./launch.js";
import { initialize } from "./ui.js";
await initialize(async () => {
  if (!can("instance.batchCreate")) {
    app.append(
      el(
        "p",
        "Classroom launches require instructor access and an environment granted to this project. Group members can deploy shared labs from Teams.",
      ),
    );
  } else {
    const selected = new Set(),
      selectedLabs = new Set(),
      results = new Map();
    let roster = [],
      labs = [],
      batchId = "",
      pending = false,
      rendered = "",
      targetId = "";
    const launch = details("Launch classroom labs");
    const form = await launchForm(launch, {
      batch: true,
      owners: () => [...selected],
      onlaunch: async (result) => {
        if (result.changed) {
          targetId = result.targetId;
          selected.clear();
          await refresh();
        } else {
          batchId = result.id;
          message("Classroom launch accepted.");
          await refresh();
        }
      },
    });
    targetId = form.target.value;
    const students = section("Students & labs");
    students.append(
      el(
        "p",
        "Select students to launch their own labs. Select labs to end them together.",
      ),
    );
    const allStudents = check(students, "Select all students");
    allStudents.onchange = () => {
      selected.clear();
      if (allStudents.checked) roster.forEach((p) => selected.add(p.id));
      render();
    };
    const ended = check(students, "Show ended labs");
    ended.onchange = () => render();
    const actions = el("div", "", { class: "actions" });
    students.append(actions);
    const list = el("div", "", { class: "grid" });
    students.append(list);
    actions.append(
      button(
        "End selected labs",
        async () => {
          const chosen = labs.filter(
            (l) => selectedLabs.has(l.id) && l.phase !== "destroyed",
          );
          if (!chosen.length) throw new Error("Select labs to end.");
          if (
            !(await confirm(
              `End ${chosen.length} labs?`,
              "This permanently deletes these labs and their files.",
              chosen.map(
                (l) =>
                  `${roster.find((p) => p.id === l.ownerId)?.displayName ?? l.ownerId} · ${l.id}`,
              ),
            ))
          )
            return;
          let accepted = 0,
            failed = 0;
          for (const lab of chosen) {
            try {
              const response = await mutate("labs.destroy", {
                instanceId: lab.id,
              });
              results.set(lab.id, { operation: response.operation });
              selectedLabs.delete(lab.id);
              accepted++;
            } catch (error) {
              results.set(lab.id, { error: error.message });
              failed++;
            }
          }
          message(
            `${accepted} deletions queued.${failed ? ` ${failed} failed; select those labs to retry.` : ""}`,
          );
          await refresh();
        },
        "danger",
      ),
    );
    const batchStatus = section("Launch status");
    batchStatus.hidden = true;
    function render() {
      list.replaceChildren();
      allStudents.checked =
        roster.length > 0 && roster.every((p) => selected.has(p.id));
      const people = [
        ...roster,
        ...[...new Set(labs.map((l) => l.ownerId))]
          .filter((id) => !roster.some((p) => p.id === id))
          .map((id) => ({ id, displayName: id, unavailable: true })),
      ];
      for (const person of people) {
        const card = el("article");
        const checkbox = check(
          card,
          person.displayName,
          selected.has(person.id),
        );
        checkbox.disabled = !!person.unavailable;
        checkbox.onchange = () => {
          checkbox.checked
            ? selected.add(person.id)
            : selected.delete(person.id);
          allStudents.checked = roster.every((p) => selected.has(p.id));
        };
        card.append(
          el("small", person.username ?? "Not eligible for this environment"),
        );
        const own = labs.filter(
          (l) =>
            l.ownerId === person.id &&
            (ended.checked || l.phase !== "destroyed"),
        );
        if (!own.length) card.append(el("p", "No active labs"));
        for (const lab of own) {
          const row = el("div");
          if (lab.phase !== "destroyed") {
            const select = check(
              row,
              `Select lab ${lab.id}`,
              selectedLabs.has(lab.id),
            );
            select.disabled =
              !!results.get(lab.id)?.operation ||
              (lab.desiredLifecycle === "destroyed" &&
                !["failed", "degraded"].includes(lab.phase));
            select.onchange = () =>
              select.checked
                ? selectedLabs.add(lab.id)
                : selectedLabs.delete(lab.id);
          }
          labCard(lab, row, { end: true, onchange: refresh });
          const result = results.get(lab.id);
          if (result?.error)
            row.append(
              el("p", result.error, { role: "alert", class: "error" }),
            );
          if (result?.operation)
            row.append(el("p", `Deletion ${result.operation.state}`));
          card.append(row);
        }
        list.append(card);
      }
      if (!people.length)
        list.append(
          el(
            "p",
            "No eligible students are assigned to this workspace and environment.",
          ),
        );
    }
    async function refresh() {
      if (pending || !targetId) return;
      pending = true;
      try {
        [roster, labs] = await Promise.all([
          all("members.list", { targetId }, "after"),
          all("labs.list"),
        ]);
        for (const id of selected)
          if (!roster.some((p) => p.id === id)) selected.delete(id);
        for (const [id, result] of results) {
          if (
            result.operation &&
            !["succeeded", "failed", "canceled"].includes(
              result.operation.state,
            )
          ) {
            const op = await request("operations.get", {
              operationId: result.operation.id,
            });
            results.set(
              id,
              ["failed", "canceled"].includes(op.state)
                ? { error: op.message || "Deletion failed. Select to retry." }
                : { operation: op },
            );
          }
        }
        if (batchId) {
          batchStatus.hidden = false;
          const batch = await request("labs.batchGet", { batchId });
          batchStatus.replaceChildren(el("h2", "Launch status"));
          for (const item of batch.items ?? [])
            batchStatus.append(
              el(
                "p",
                `${roster.find((p) => p.id === item.ownerId)?.displayName ?? item.ownerId}: ${item.operation.state}${item.operation.message ? ` · ${item.operation.message}` : ""}`,
              ),
            );
        }
        const next = JSON.stringify([
          targetId,
          roster,
          labs,
          [...results],
          [...selected],
        ]);
        if (next !== rendered) {
          rendered = next;
          render();
        }
      } finally {
        pending = false;
      }
    }
    actions.append(button("Refresh classroom", refresh));
    await refresh();
    poll(refresh);
  }
});
