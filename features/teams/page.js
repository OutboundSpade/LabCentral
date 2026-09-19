import {
  app,
  el,
  section,
  details,
  field,
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
import { launchForm, preparations } from "./launch.js";
import { initialize } from "./ui.js";
await initialize(async () => {
  app.prepend(
    el(
      "p",
      "Join one group in this project to share its labs and VM consoles. Leaving keeps the labs and ends your access.",
    ),
  );
  const groupsPanel = section("Project groups"),
    groupList = el("div", "", { class: "grid" });
  groupsPanel.append(groupList);
  let groups = [],
    selected = null,
    labs = [],
    pending = false,
    rendered = "";
  const launch = details("Deploy a group lab");
  const form = await launchForm(launch, {
    owner: () => (selected ? { kind: "team", id: selected.id } : undefined),
    onlaunch: async (work) => {
      if (!work.changed)
        message(
          "Team-owned preparation queued. Membership is checked again before deployment.",
        );
    },
  });
  const labsPanel = section("Your group labs"),
    labList = el("div", "", { class: "grid" });
  labsPanel.append(labList);
  async function refresh() {
    if (pending) return;
    pending = true;
    try {
      [groups, labs] = await Promise.all([
        all("groups.list", {}, "after"),
        all("labs.list"),
      ]);
      const next = JSON.stringify([groups, labs]);
      if (next === rendered) return;
      rendered = next;
      selected = groups.find((g) => g.joined) ?? null;
      groupList.replaceChildren();
      if (!groups.length)
        groupList.append(
          el("p", "No groups have been created for this project."),
        );
      for (const group of groups) {
        const card = el("article");
        card.append(
          el("h3", group.name),
          el(
            "p",
            `${group.memberCount} members · ${group.enrollmentOpen ? "Enrollment open" : "Enrollment closed"}${group.joined ? " · Your group" : ""}`,
          ),
        );
        const actions = el("div", "", { class: "actions" });
        const membership = button(
          group.joined ? "Leave group" : "Join group",
          async () => {
            if (
              group.joined &&
              !(await confirm(
                "Leave this group?",
                "Your access to existing group labs and consoles will end. The labs will remain.",
                [group.name],
              ))
            )
              return;
            await mutate(group.joined ? "groups.leave" : "groups.join", {
              groupId: group.id,
            });
            await refresh();
          },
        );
        membership.disabled =
          !group.joined && (!group.enrollmentOpen || !!selected);
        actions.append(membership);
        if (can("team.manage"))
          actions.append(
            button(
              group.enrollmentOpen ? "Close enrollment" : "Open enrollment",
              async () => {
                await mutate("groups.enrollment", {
                  groupId: group.id,
                  open: !group.enrollmentOpen,
                  resourceVersion: group.resourceVersion,
                });
                await refresh();
              },
            ),
          );
        card.append(actions);
        groupList.append(card);
      }
      launch.hidden = !selected;
      labList.replaceChildren();
      if (!selected)
        labList.append(el("p", "Join a group to deploy and access its labs."));
      else {
        const own = labs.filter(
          (l) =>
            l.ownerKind === "team" &&
            l.ownerId === selected.id &&
            l.phase !== "destroyed",
        );
        if (!own.length)
          labList.append(el("p", "Your group has no active labs."));
        for (const lab of own)
          labCard(lab, labList, { end: true, onchange: refresh });
      }
    } finally {
      pending = false;
    }
  }
  if (can("team.manage")) {
    const admin = details("Create project groups");
    const names = field(
      admin,
      "Group names — one per line (up to 100)",
      "textarea",
      { maxlength: 12900 },
    );
    const open = check(admin, "Open enrollment", true);
    admin.append(
      button(
        "Create groups",
        async () => {
          const list = names.value
            .split("\n")
            .map((x) => x.trim())
            .filter(Boolean);
          if (!list.length || list.length > 100)
            throw new Error("Enter 1–100 group names.");
          await mutate("groups.create", {
            names: list,
            enrollmentOpen: open.checked,
          });
          names.value = "";
          message("Groups created.");
          await refresh();
        },
        "primary",
      ),
    );
  }
  groupsPanel.append(button("Refresh groups", refresh));
  await refresh();
  poll(refresh);

  preparations(app);
});
