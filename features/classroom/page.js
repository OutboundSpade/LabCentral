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
function openLab(lab) {
  const control = button(lab.name || "Open lab", () =>
    request("navigation.lab", { instanceId: lab.id }),
  );
  control.classList.remove("whitespace-nowrap");
  control.classList.add("whitespace-normal", "max-w-full", "break-words", "text-left");
  return control;
}
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
      batches = [],
      groups = [],
      groupMembers = [],
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
          launch.close();
          views.select("Launch activity");
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
    if (!targetId)
      students.append(
        el(
          "p",
          "No deployment environment is available. Ask an administrator to grant an environment to this workspace.",
          { role: "status" },
        ),
      );
    search.oninput = () => render();
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
                `${roster.find((p) => p.id === l.ownerId)?.displayName ?? l.ownerId} · ${l.name || "Untitled lab"}`,
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
    groupPanel.append(
      button("Manage groups & deploy", () => navigate("teams"), "primary"),
      groupList,
    );
    const batchStatus = section(
      "Launch status",
      views.panels["Launch activity"],
    );
    batchStatus.append(el("p", "Loading recent launches…"));
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
        teams: groups
          .filter((g) =>
            groupMembers.some((m) => m.groupId === g.id && m.id === p.id),
          )
          .map((g) => ({
            ...g,
            labs: labs.filter(
              (l) =>
                l.ownerKind === "team" &&
                l.ownerId === g.id &&
                l.phase !== "destroyed",
            ),
          })),
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
          own.append(el("p", "No personal labs", { class: "muted" }));
        for (const group of person.teams) {
          const shared = el("div", "", {
            class: "border-l-2 border-primary pl-3 space-y-2",
          });
          shared.append(el("h3", group.name));
          for (const lab of group.labs)
            shared.append(
              openLab(lab),
            );
          if (!group.labs.length)
            shared.append(el("small", "No active group labs."));
          own.append(shared);
        }
        for (const lab of person.own) {
          const row = el("div", "", {
            class: "border-l border-base-300 pl-3 space-y-3",
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
            openLab(lab),
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
        return labCard(
          {
            ...l,
            ownerName:
              l.ownerName ||
              groups.find((g) => g.id === l.ownerId)?.name ||
              "Group",
          },
          holder,
          { end: true, onchange: refresh },
        );
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
        [roster, labs, groups, { items: batches }] = await Promise.all([
          all("members.list", { targetId }, "after"),
          all("labs.list"),
          all("groups.list", {}, "after"),
          request("labs.batchList"),
        ]);
        groupMembers = [];
        // Only administrators/enrolled users can see group rosters; absent authority
        // must not make the rest of Classroom unusable.
        for (const g of groups) {
          const members = await all(
            "groups.members",
            { groupId: g.id },
            "after",
          );
          groupMembers.push(...members.map((m) => ({ ...m, groupId: g.id })));
        }
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
        const batchRows = batches.map((b) => ({
          ...b,
          items: (b.items ?? []).map((item) => ({
            ...item,
            label:
              roster.find((p) => p.id === item.ownerId)?.displayName ||
              item.ownerId,
            lab: labs.find((l) => l.id === item.instanceId),
          })),
        }));
        let history = batchStatus.querySelector("[data-history]");
        if (!history) {
          history = el("div", "", { "data-history": "", class: "space-y-3" });
          batchStatus.replaceChildren(el("h2", "Recent launches"), history);
        }
        stableList(history, batchRows, (b) => {
          const card = el("article");
          card.append(
            el("h3", b.items[0]?.lab?.name || "Classroom launch"),
            el("small", new Date(b.createdAt).toLocaleString()),
          );
          for (const item of b.items) {
            const row = el("div", "", { class: "actions" });
            row.append(
              el(
                "span",
                `${item.label} · ${item.lab?.phase || item.operation.state}`,
              ),
            );
            if (item.instanceId)
              row.append(
                button("Open lab", () =>
                  request("navigation.lab", { instanceId: item.instanceId }),
                ),
              );
            card.append(row);
          }
          return card;
        });
        if (!batchRows.length)
          history.replaceChildren(el("p", "No recent launches."));
        const next = JSON.stringify([
          targetId,
          roster,
          labs,
          groups,
          groupMembers,
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
