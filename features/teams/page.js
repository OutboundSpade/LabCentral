import {
  app,
  el,
  section,
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
  initialize,
  tabs,
  modal,
  options,
  stableList,
} from "./ui.js";
import { teamManagement } from "./team-management.js";
import { launchForm, preparations } from "./launch.js";
await initialize(async () => {
  const admin = can("team.manage"),
    views = tabs(
      admin
        ? ["Manage", "Labs", "Your team", "Groups"]
        : ["Your team", "Groups"],
    );
  let groups = [],
    labs = [],
    joined = null,
    pending = false,
    launchOwner = null,
    managedID = "";
  let refreshBulk = () => {};
  const current = section("Your team", views.panels["Your team"]),
    currentInfo = el("div"),
    currentLabs = el("div", "", { class: "space-y-3" });
  current.append(currentInfo);
  const teamColumns = el("div", "", {
    class: "grid gap-6 md:grid-cols-[minmax(0,1fr)_14rem]",
  });
  views.panels["Your team"].append(teamColumns);
  section("Labs", teamColumns).append(currentLabs);
  const roster = section("Members", teamColumns),
    memberList = el("div");
  roster.append(memberList);

  const launch = modal("Deploy a group lab"),
    ownerLabel = el("p", "", { class: "font-semibold" });
  launch.body.append(ownerLabel);
  const form = await launchForm(launch.body, {
    owner: () =>
      launchOwner ? { kind: "team", id: launchOwner.id } : undefined,
    onlaunch: async (w) => {
      if (!w.changed) {
        launch.close();
        message("Lab preparation started.");
        await refresh();
        await refreshPreparations();
      }
    },
  });
  form.actions.append(button("Cancel", () => launch.close()));
  const openLaunch = async (group) => {
    launchOwner = group;
    ownerLabel.textContent = `Group: ${group.name}`;
    await form.reload();
    launch.open();
  };
  const activeOwner = () =>
    admin && views.panels.Manage && !views.panels.Manage.hidden
      ? managedID
      : joined?.id;
  const progressParent = admin
    ? views.panels.Manage
    : views.panels["Your team"];
  const refreshPreparations = preparations(
    progressParent,
    (w) =>
      w.request.preparation?.owner?.kind === "team" &&
      ((admin && views.panels["Your team"].hidden) ||
        w.request.preparation.owner.id === activeOwner()),
    { activeOnly: true },
  );
  app.addEventListener("tabchange", (event) => {
    if (["Manage", "Your team"].includes(event.detail)) {
      views.panels[event.detail].append(refreshPreparations.panel);
      void refreshPreparations().catch((e) => message(e.message, true));
    }
  });
  const discovery = section("Project groups", views.panels.Groups),
    search = field(discovery, "Search groups", "input", {
      type: "search",
      placeholder: "Find a group",
    }),
    groupList = el("div", "", { class: "grid" });
  discovery.append(groupList);
  async function membership(g) {
    if (
      g.joined &&
      !(await confirm(
        "Leave this group?",
        "Your access to group labs and consoles will end.",
        [g.name],
        "Leave group",
      ))
    )
      return;
    await mutate(g.joined ? "groups.leave" : "groups.join", { groupId: g.id });
    await refresh();
    if (!g.joined) views.select("Your team");
    await refreshPreparations();
  }
  function renderGroups() {
    const shown = groups
      .filter((g) => g.name.toLowerCase().includes(search.value.toLowerCase()))
      .map((g) => ({ ...g, otherJoined: !!joined && !g.joined }));
    stableList(groupList, shown, (g) => {
      const card = el("article"),
        actions = el("div", "", { class: "actions" });
      card.append(
        el("h3", g.name),
        el(
          "p",
          `${g.memberCount} members · ${g.enrollmentOpen ? "Enrollment open" : "Enrollment closed"}`,
        ),
      );
      const join = button(
        g.joined ? "Leave group" : "Join group",
        () => membership(g),
        g.joined ? "" : "primary",
      );
      join.disabled = !g.joined && (!g.enrollmentOpen || g.otherJoined);
      actions.append(join);
      card.append(actions);
      if (g.otherJoined)
        card.append(
          el("small", "Leave your current group before joining another."),
        );
      return card;
    });
    if (!shown.length)
      groupList.replaceChildren(
        el("p", groups.length ? "No matching groups." : "No groups yet."),
      );
  }
  search.oninput = renderGroups;
  async function members(g, parent) {
    if (!g) {
      parent.replaceChildren();
      return;
    }
    const items = await all("groups.members", { groupId: g.id }, "after");
    stableList(parent, items, (m) => el("p", m.displayName || "Team member"));
    if (!items.length) parent.replaceChildren(el("p", "No members yet."));
  }
  function showLabs(g, parent) {
    const own = labs.filter(
      (l) =>
        g &&
        l.ownerKind === "team" &&
        l.ownerId === g.id &&
        l.phase !== "destroyed",
    );
    stableList(parent, own, (l) =>
      labCard({ ...l, ownerName: g.name }, el("div"), {
        end: true,
        onchange: refresh,
      }),
    );
    if (!own.length)
      parent.replaceChildren(
        el("p", g ? "No active labs." : "Join a group to access its labs."),
      );
  }
  let openCreateGroups;
  let manageGroup,
    manageInfo,
    manageMembers,
    manageLabs,
    manageFingerprint = "";
  async function refreshManaged() {
    if (!manageGroup) return;
    const g = groups.find((g) => g.id === manageGroup.value);
    managedID = g?.id ?? "";
    const sig = JSON.stringify([
      g,
      labs
        .filter((l) => l.ownerId === managedID && l.phase !== "destroyed")
        .map((l) => l.id),
    ]);
    if (sig !== manageFingerprint) {
      manageFingerprint = sig;
      manageInfo.replaceChildren();
      if (g) {
        const actions = el("div", "", { class: "actions" });
        manageInfo.append(
          el(
            "p",
            `${g.memberCount} members · ${g.enrollmentOpen ? "Enrollment open" : "Enrollment closed"}`,
          ),
          actions,
        );
        actions.append(
          button("Deploy group lab", () => openLaunch(g), "primary"),
          button(
            g.enrollmentOpen ? "Close enrollment" : "Open enrollment",
            async () => {
              await mutate("groups.enrollment", {
                groupId: g.id,
                open: !g.enrollmentOpen,
                resourceVersion: g.resourceVersion,
              });
              await refresh();
            },
          ),
          button(
            "Delete group",
            async () => {
              if (
                labs.some(
                  (l) =>
                    l.ownerKind === "team" &&
                    l.ownerId === g.id &&
                    l.phase !== "destroyed",
                )
              )
                throw new Error(
                  "End all active group labs before deleting this group.",
                );
              if (
                !(await confirm(
                  "Delete this group?",
                  `${g.memberCount} members will lose enrollment. Lab history is retained.`,
                  [g.name],
                  "Delete group",
                ))
              )
                return;
              await mutate("groups.delete", {
                groupId: g.id,
                resourceVersion: g.resourceVersion,
              });
              manageGroup.value = "";
              await refresh();
              message("Group deleted.");
            },
            "danger",
          ),
        );
      }
    }
    showLabs(g, manageLabs);
    await members(g, manageMembers);
  }
  if (admin) {
    const oversight = section("Group details", views.panels.Manage);
    views.panels.Manage.prepend(oversight);
    const create = modal("Create project groups"),
      names = field(
        create.body,
        "Group names — one per line (up to 100)",
        "textarea",
        { maxlength: 12900 },
      ),
      open = check(create.body, "Open enrollment", true);
    const createActions = el("div", "", { class: "actions" });
    create.body.append(createActions);
    createActions.append(
      button(
        "Create groups",
        async () => {
          const list = names.value
            .split("\n")
            .map((x) => x.trim())
            .filter(Boolean);
          if (!list.length || list.length > 100) {
            names.focus();
            throw new Error("Enter 1–100 group names.");
          }
          const result = await mutate("groups.create", {
            names: list,
            enrollmentOpen: open.checked,
          });
          names.value = "";
          create.close();
          await refresh();
          manageGroup.value =
            result.items?.[0]?.id ??
            groups.find((g) => g.name === list[0])?.id ??
            "";
          await refreshManaged();
          message("Groups created.");
        },
        "primary",
      ),
      button("Cancel", () => create.close()),
    );
    openCreateGroups = () => create.open();
    manageGroup = field(oversight, "Group", "select");
    manageInfo = el("div");
    oversight.append(manageInfo);
    const columns = el("div", "", {
      class: "grid gap-6 md:grid-cols-[minmax(0,1fr)_16rem]",
    });
    views.panels.Manage.append(columns);
    manageLabs = el("div", "", { class: "space-y-3" });
    section("Group labs", columns).append(manageLabs);
    manageMembers = el("div");
    section("Members", columns).append(manageMembers);
    manageGroup.onchange = () => {
      void refreshManaged()
        .then(refreshPreparations)
        .catch((e) => message(e.message, true));
    };
  }
  if (admin)
    refreshBulk = await teamManagement(
      views,
      () => ({ groups, labs }),
      refresh,
      () => openCreateGroups(),
    );
  let currentFingerprint = "";
  async function refresh() {
    if (pending) return;
    pending = true;
    try {
      [groups, labs] = await Promise.all([
        all("groups.list", {}, "after"),
        all("labs.list"),
      ]);
      joined = groups.find((g) => g.joined) ?? null;
      const sig = JSON.stringify(joined);
      if (sig !== currentFingerprint) {
        currentFingerprint = sig;
        currentInfo.replaceChildren();
        if (joined) {
          const g = joined,
            actions = el("div", "", { class: "actions" });
          currentInfo.append(el("h3", g.name), actions);
          actions.append(
            button("Deploy a group lab", () => openLaunch(g), "primary"),
            button("Leave group", () => membership(g)),
          );
        } else
          currentInfo.append(
            button("Find a group", () => views.select("Groups"), "primary"),
          );
      }
      roster.hidden = !joined;
      showLabs(joined, currentLabs);
      renderGroups();
      refreshBulk();
      await members(joined, memberList);
      if (manageGroup) {
        const old = manageGroup.value;
        options(manageGroup, groups);
        manageGroup.value = groups.some((g) => g.id === old)
          ? old
          : (groups[0]?.id ?? "");
        await refreshManaged();
      }
    } finally {
      pending = false;
    }
  }
  discovery.append(button("Refresh groups", refresh));
  await refresh();
  await refreshPreparations();
  poll(refresh);
});
