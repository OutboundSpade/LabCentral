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
  tabs,
  modal,
  field,
  navigate,
  stableList,
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
    app.append(
      button("Go to Teams", () => navigate("teams"), "primary"),
      button("Go to My labs", () => navigate("labs")),
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
    const views = tabs(["Students", "Group labs", "Launch activity"]);
    const launch = modal(
      "Launch classroom labs",
      "Review the selected students, template and environment before launching.",
    );
    const selectedSummary = el("p");
    launch.body.append(selectedSummary);
    const form = await launchForm(launch.body, {
      batch: true,
      owners: () => [...selected],
      onlaunch: async (result) => {
        if (result.changed) {
          targetId = result.targetId;
          selected.clear();
          selectedLabs.clear();
          await refresh();
        } else {
          batchId = result.id;
          message("Classroom launch accepted.");
          await refresh();
        }
      },
    });
    launch.body.append(button("Close", () => launch.close()));
    targetId = form.target.value;
    const students = section("Students & labs", views.panels.Students);
    const search = field(students, "Search students", "input", {
      type: "search",
      placeholder: "Search by name or username",
    });
    const visibleRoster = () =>
      roster.filter((p) =>
        (p.displayName + " " + p.username)
          .toLowerCase()
          .includes(search.value.toLowerCase()),
      );
    students.prepend(form.target.parentElement);
    if(!targetId)students.append(el("p","No deployment environment is available. Ask an administrator to grant an environment to this workspace.",{role:"status"}));
    search.oninput = () => render();
    students.append(
      el(
        "p",
        "Select students to launch their own labs. Select labs to end them together.",
      ),
    );
    const allStudents = check(students, "Select all students");
    allStudents.onchange = () => {
      selected.clear();
      if (allStudents.checked)
        visibleRoster().forEach((p) => selected.add(p.id));
      render();
    };
    const ended = check(students, "Show ended labs");
    ended.onchange = () => render();
    const actions = el("div", "", {
      class:
        "sticky top-12 z-10 bg-base-100 border-y border-base-300 py-3 flex flex-wrap items-center gap-3",
    });
    students.append(actions);
    const list = el("div", "", { class: "space-y-2" });
    students.append(list);
    const endSelected = button(
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
            "End selected labs",
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
    );
    const selectionCount = el("span", "", { role: "status", class: "text-sm" });
    const launchSelected = button(
      "Launch selected labs",
      () => {
        selectedSummary.textContent = `${selected.size} students · Environment: ${form.target.selectedOptions[0]?.textContent ?? "Unavailable"}: ${roster
          .filter((p) => selected.has(p.id))
          .map((p) => p.displayName)
          .join(", ")}`;
        launch.open();
      },
      "primary",
    );
    actions.append(selectionCount, launchSelected, endSelected);
    const allLabs = check(students, "Select all active labs");
    allLabs.onchange = () => {
      selectedLabs.clear();
      if (allLabs.checked)
        for (const lab of labs.filter(
          (l) =>
            l.ownerKind === "user" &&
            visibleRoster().some((p) => p.id === l.ownerId) &&
            l.phase !== "destroyed" &&
            l.desiredLifecycle !== "destroyed",
        ))
          selectedLabs.add(lab.id);
      render();
    };
    const groupPanel = section("Group-owned labs", views.panels["Group labs"]),
      groupList = el("div", "", { class: "space-y-3" });
    groupPanel.append(groupList);
    const batchStatus = section(
      "Launch status",
      views.panels["Launch activity"],
    );
    batchStatus.append(
      el(
        "p",
        "No launches in this visit yet. Existing labs appear in the roster.",
      ),
    );
    function counts() {
      selectionCount.textContent = `${selected.size} students selected · ${selectedLabs.size} labs selected`;
      launchSelected.disabled = !selected.size;
      endSelected.disabled = !selectedLabs.size;
      allStudents.checked =
        visibleRoster().length > 0 &&
        visibleRoster().every((p) => selected.has(p.id));
      allStudents.indeterminate = selected.size > 0 && !allStudents.checked;
    }
    function render() {
      const activeOwners = labs
        .filter(
          (l) =>
            l.ownerKind === "user" &&
            (ended.checked || l.phase !== "destroyed"),
        )
        .map((l) => l.ownerId);
      const people = [
        ...visibleRoster(),
        ...[...new Set(activeOwners)]
          .filter((id) => !roster.some((p) => p.id === id))
          .map((id) => ({
            id,
            displayName: "Former participant",
            unavailable: true,
          })),
      ];
      const rows = people.map((p) => ({
        ...p,
        selected: selected.has(p.id),
        own: labs
          .filter(
            (l) =>
              l.ownerKind === "user" &&
              l.ownerId === p.id &&
              (ended.checked || l.phase !== "destroyed"),
          )
          .map((l) => ({
            ...l,
            selected: selectedLabs.has(l.id),
            outcome: results.get(l.id),
          })),
      }));
      stableList(list, rows, (person) => {
        const card = el("article", "", { class: "grid gap-3 md:grid-cols-2" }),
          identity = el("div");
        card.append(identity);
        const checkbox = check(identity, person.displayName, person.selected);
        checkbox.disabled = !!person.unavailable;
        checkbox.onchange = () => {
          checkbox.checked
            ? selected.add(person.id)
            : selected.delete(person.id);
          counts();
        };
        identity.append(
          el("small", person.username ?? "Not eligible for this environment"),
        );
        const own = el("div", "", { class: "space-y-2" });
        card.append(own);
        if (!person.own.length)
          own.append(el("p", "No active labs", { class: "muted" }));
        for (const lab of person.own) {
          const row = el("div", "", {
            class: "border-l border-base-300 pl-3 space-y-2",
          });
          if (lab.phase !== "destroyed") {
            const choice = check(
              row,
              `Select lab ${lab.name ?? lab.id}`,
              lab.selected,
            );
            choice.disabled =
              !!lab.outcome?.operation ||
              (lab.desiredLifecycle === "destroyed" &&
                !["failed", "degraded"].includes(lab.phase));
            choice.onchange = () => {
              choice.checked
                ? selectedLabs.add(lab.id)
                : selectedLabs.delete(lab.id);
              counts();
            };
          }
          row.append(
            button(lab.name ?? "Open lab", () =>
              request("navigation.lab", { instanceId: lab.id }),
            ),
            el("small", lab.phase),
          );
          if (lab.outcome?.error)
            row.append(
              el("p", lab.outcome.error, { role: "alert", class: "error" }),
            );
          if (lab.outcome?.operation)
            row.append(el("p", `Deletion ${lab.outcome.operation.state}`));
          own.append(row);
        }
        return card;
      });
      if (!people.length)
        list.replaceChildren(
          el(
            "p",
            "No eligible students match this workspace, environment and search.",
          ),
        );
      const groupLabs = labs.filter(
        (l) =>
          l.ownerKind === "team" && (ended.checked || l.phase !== "destroyed"),
      );
      stableList(groupList, groupLabs, (l) => {
        const holder = el("div");
        return labCard(l, holder, { end: true, onchange: refresh });
      });
      if (!groupLabs.length)
        groupList.replaceChildren(
          el(
            "p",
            "No active group labs. Manage groups and enrollment in Teams.",
          ),
          button("Go to Teams", () => navigate("teams")),
        );
      counts();
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
          search.value,
          ended.checked,
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
