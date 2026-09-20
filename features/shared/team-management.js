import {
  el,
  field,
  check,
  button,
  modal,
  confirm,
  mutate,
  message,
  stableList,
  labCard,
} from "./ui.js";
import { launchForm } from "./launch.js";

// Bulk workflows issue bounded, individually authorized commands. Failed commands
// retain their original payload and idempotency key; successes are never retried.
export async function teamManagement(views, read, refresh, createGroup) {
  const selectedGroups = new Set(),
    selectedLabs = new Set();
  let busy = false,
    retry = [],
    retryAction,
    refreshView = () => {},
    launchGroups = [];
  const root = el("section");
  const heading = el("div", "", {
    class: "flex flex-wrap items-center justify-between gap-3",
  });
  heading.append(
    el("h2", "Groups"),
    button("Create groups", createGroup, "primary"),
  );
  root.append(heading);
  views.panels.Manage.prepend(root);
  const search = field(root, "Filter groups", "input", { type: "search" });
  const selectGroups = check(root, "Select all matching groups");
  const groupList = el("div", "", { class: "grid gap-3 md:grid-cols-2" });
  const outcomes = el("div", "", { role: "status", class: "space-y-2" });
  root.append(groupList, outcomes);
  const groupBar = el("div", "", {
    class:
      "flex flex-wrap items-center gap-3 border-b border-base-300 bg-base-100 py-3",
  });
  const groupCount = el("span"),
    controls = [];
  groupBar.append(groupCount);
  views.toolbar("Manage", groupBar);
  const addAction = (label, fn, kind = "") => {
    const b = button(label, fn, kind, () => busy || !selectedGroups.size);
    controls.push(b);
    groupBar.append(b);
    return b;
  };
  const chosenGroups = () =>
    read().groups.filter((g) => selectedGroups.has(g.id));
  const matchingGroups = () =>
    read().groups.filter((g) =>
      g.name.toLowerCase().includes(search.value.toLowerCase()),
    );
  async function run(commands) {
    if (busy) return;
    if (!commands.length || commands.length > 100)
      throw new Error("Select between 1 and 100 items.");
    if (views.panels.Labs.hidden) root.append(outcomes);
    busy = true;
    refreshView();
    retry = [];
    retryAction = undefined;
    outcomes.replaceChildren();
    const progress = el("p", `0 of ${commands.length} completed`);
    outcomes.append(progress);
    let accepted = 0;
    try {
      for (const c of commands) {
        try {
          await mutate(c.method, c.input);
          accepted++;
          c.selection.delete(c.id);
        } catch (e) {
          retry.push(c);
          outcomes.append(
            el("p", `${c.name}: ${e.message}`, {
              class: "text-error break-words",
            }),
          );
        }
        progress.textContent = `${accepted + retry.length} of ${commands.length} completed`;
        refreshView();
      }
      progress.textContent = `${accepted} accepted · ${retry.length} failed`;
      if (retry.length) {
        retryAction = button(
          "Retry failed actions",
          () => run([...retry]),
          "",
          () => busy,
        );
        retryAction.disabled = true;
        outcomes.append(retryAction);
      }
      await refresh();
    } finally {
      busy = false;
      refreshView();
    }
  }
  const launch = modal("Deploy group labs");
  const launchSummary = el("p");
  launch.body.append(launchSummary);
  const form = await launchForm(launch.body, {
    prepareMany: async (input) => {
      cancel.disabled = true;
      try {
        await run(
          launchGroups.map((g) => ({
            id: g.id,
            name: g.name,
            selection: selectedGroups,
            method: "labs.prepare",
            input: { ...input, owner: { kind: "team", id: g.id } },
          })),
        );
        launch.close();
        message(
          "Group preparation requests completed. Review progress and any failures below.",
        );
      } finally {
        cancel.disabled = false;
      }
    },
  });
  const cancel = button("Cancel", () => launch.close());
  form.actions.append(cancel);
  addAction(
    "Deploy selected groups",
    async () => {
      launchGroups = chosenGroups();
      if (!launchGroups.length || launchGroups.length > 100)
        throw new Error("Select between 1 and 100 groups.");
      launchSummary.textContent = `${launchGroups.length} groups selected`;
      await form.reload();
      await launch.open();
    },
    "primary",
  );
  async function enrollment(open) {
    const groups = chosenGroups();
    if (
      !(await confirm(
        open ? "Open selected groups?" : "Close selected groups?",
        "Existing members keep their access.",
        groups.map((g) => g.name),
        open ? "Open enrollment" : "Close enrollment",
      ))
    )
      return;
    await run(
      groups.map((g) => ({
        id: g.id,
        name: g.name,
        selection: selectedGroups,
        method: "groups.enrollment",
        input: { groupId: g.id, open, resourceVersion: g.resourceVersion },
      })),
    );
  }
  addAction("Open selected enrollment", () => enrollment(true));
  addAction("Close selected enrollment", () => enrollment(false));
  addAction(
    "Delete selected groups",
    async () => {
      const groups = chosenGroups();
      if (
        !(await confirm(
          "Delete selected groups?",
          "Members lose enrollment. Groups with active labs cannot be deleted.",
          groups.map((g) => g.name),
          "Delete groups",
        ))
      )
        return;
      await run(
        groups.map((g) => ({
          id: g.id,
          name: g.name,
          selection: selectedGroups,
          method: "groups.delete",
          input: { groupId: g.id, resourceVersion: g.resourceVersion },
        })),
      );
    },
    "danger",
  );
  const labsPanel = views.panels.Labs;
  const labSearch = field(labsPanel, "Filter team labs", "input", {
    type: "search",
  });
  const selectLabs = check(labsPanel, "Select all matching labs");
  const labList = el("div", "", { class: "space-y-3" });
  labsPanel.append(labList);
  const labBar = el("div", "", {
      class:
        "flex flex-wrap items-center gap-3 border-b border-base-300 bg-base-100 py-3",
    }),
    labCount = el("span");
  const end = button(
    "End selected labs",
    async () => {
      const labs = read().labs.filter((l) => selectedLabs.has(l.id));
      if (
        !(await confirm(
          "End selected team labs?",
          "This deletes the selected labs and their files.",
          labs.map((l) => `${l.ownerName || "Team"} · ${l.name || "Lab"}`),
          "End labs",
        ))
      )
        return;
      labsPanel.prepend(outcomes);
      await run(
        labs.map((l) => ({
          id: l.id,
          name: l.name || "Lab",
          selection: selectedLabs,
          method: "labs.destroy",
          input: { instanceId: l.id },
        })),
      );
    },
    "danger",
    () => busy || !selectedLabs.size,
  );
  labBar.append(labCount, end);
  views.toolbar("Labs", labBar);
  const matchingLabs = () =>
    read().labs.filter(
      (l) =>
        l.ownerKind === "team" &&
        l.phase !== "destroyed" &&
        l.desiredLifecycle !== "destroyed" &&
        `${l.name} ${l.ownerName}`
          .toLowerCase()
          .includes(labSearch.value.toLowerCase()),
    );
  selectGroups.onchange = () => {
    selectedGroups.clear();
    if (selectGroups.checked)
      matchingGroups()
        .slice(0, 100)
        .forEach((g) => selectedGroups.add(g.id));
    refreshView();
  };
  selectLabs.onchange = () => {
    selectedLabs.clear();
    if (selectLabs.checked)
      matchingLabs()
        .slice(0, 100)
        .forEach((l) => selectedLabs.add(l.id));
    refreshView();
  };
  search.oninput = labSearch.oninput = () => refreshView();
  refreshView = () => {
    const { groups, labs } = read();
    for (const id of selectedGroups)
      if (!groups.some((g) => g.id === id)) selectedGroups.delete(id);
    for (const id of selectedLabs)
      if (
        !labs.some(
          (l) =>
            l.id === id &&
            l.phase !== "destroyed" &&
            l.desiredLifecycle !== "destroyed",
        )
      )
        selectedLabs.delete(id);
    groupCount.textContent = `${selectedGroups.size} groups selected`;
    labCount.textContent = `${selectedLabs.size} labs selected`;
    controls.forEach((b) => (b.disabled = busy || !selectedGroups.size));
    end.disabled = busy || !selectedLabs.size;
    if (retryAction) retryAction.disabled = busy;
    stableList(
      groupList,
      matchingGroups().map((g) => ({
        ...g,
        selected: selectedGroups.has(g.id),
        busy,
      })),
      (g) => {
        const row = el("article");
        const choice = check(row, g.name, g.selected);
        choice.disabled = busy;
        choice.onchange = () => {
          choice.checked
            ? selectedGroups.add(g.id)
            : selectedGroups.delete(g.id);
          refreshView();
        };
        row.append(
          el(
            "small",
            `${g.memberCount} members · ${g.enrollmentOpen ? "Open" : "Closed"}`,
          ),
        );
        return row;
      },
    );
    stableList(
      labList,
      matchingLabs().map((l) => ({
        ...l,
        selected: selectedLabs.has(l.id),
        busy,
      })),
      (l) => {
        const row = el("div", "", { class: "min-w-0" });
        const choice = check(
          row,
          `Select ${l.name || "lab"} · ${l.ownerName || "team"}`,
          l.selected,
        );
        choice.disabled = busy;
        choice.onchange = () => {
          choice.checked ? selectedLabs.add(l.id) : selectedLabs.delete(l.id);
          refreshView();
        };
        labCard(l, row);
        return row;
      },
    );
    for (const [input, items, selected] of [
      [selectGroups, matchingGroups(), selectedGroups],
      [selectLabs, matchingLabs(), selectedLabs],
    ]) {
      input.disabled = busy || !items.length;
      input.checked = !!items.length && items.every((i) => selected.has(i.id));
      input.indeterminate =
        !input.checked && items.some((i) => selected.has(i.id));
    }
  };
  return refreshView;
}
