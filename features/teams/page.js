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
import { launchForm, preparations } from "./launch.js";
await initialize(async () => {
  const admin = can("team.manage");
  app.append(
    el(
      "p",
      "Work together in one team per workspace. Your team keeps its labs when members leave.",
      { class: "muted" },
    ),
  );
  const views = tabs(["Your team", "Groups", ...(admin ? ["Manage"] : [])]),
    panels = views.panels;
  let groups = [],
    labs = [],
    joined = null,
    pending = false;
  const current = section("Your team", panels["Your team"]),
    currentInfo = el("div");
  current.append(currentInfo);
  const launch = modal(
    "Deploy a group lab",
    "This lab belongs to your team. Every member can access it.",
  );
  const form = await launchForm(launch.body, {
    owner: () => (joined ? { kind: "team", id: joined.id } : undefined),
    onlaunch: async (w) => {
      if (!w.changed)
        message(
          "Team preparation queued. You can close this dialog and follow its progress below.",
        );
    },
  });
  launch.body.append(button("Close", () => launch.close()));
  const deploy = button("Deploy a group lab", () => launch.open(), "primary");
  current.append(deploy);
  const roster = section("Members", panels["Your team"]),
    memberList = el("div");
  roster.append(memberList);
  const labPanel = section("Your group labs", panels["Your team"]),
    labList = el("div", "", { class: "space-y-3" });
  labPanel.append(labList);
  preparations(
    panels["Your team"],
    (w) =>
      !!joined &&
      w.request.preparation?.owner?.kind === "team" &&
      w.request.preparation.owner.id === joined.id,
  );
  const discovery = section("Project groups", panels.Groups),
    search = field(discovery, "Search groups", "input", {
      type: "search",
      placeholder: "Find a group",
    }),
    groupList = el("div", "", { class: "grid" });
  discovery.append(groupList);
  search.oninput = renderGroups;
  async function membership(group) {
    if (
      group.joined &&
      !(await confirm(
        "Leave this group?",
        "Your access to group labs and consoles will end. The labs remain.",
        [group.name],
        "Leave group",
      ))
    )
      return;
    await mutate(group.joined ? "groups.leave" : "groups.join", {
      groupId: group.id,
    });
    await refresh();
    if (!group.joined) views.select("Your team");
  }
  function renderGroups() {
    const shown = groups
      .filter((g) => g.name.toLowerCase().includes(search.value.toLowerCase()))
      .map((g) => ({ ...g, otherJoined: !!joined && !g.joined }));
    stableList(groupList, shown, (g) => {
      const card = el("article");
      card.append(
        el("h3", g.name),
        el(
          "p",
          `${g.memberCount} members · ${g.enrollmentOpen ? "Enrollment open" : "Enrollment closed"}${g.joined ? " · Your group" : ""}`,
        ),
      );
      const join = button(
        g.joined ? "Leave group" : "Join group",
        () => membership(g),
        g.joined ? "" : "primary",
      );
      join.disabled = !g.joined && (!g.enrollmentOpen || g.otherJoined);
      card.append(join);
      if (g.otherJoined)
        card.append(
          el("small", "Leave your current group before joining another."),
        );
      else if (!g.enrollmentOpen && !g.joined)
        card.append(
          el("small", "Enrollment is closed. Ask an administrator to open it."),
        );
      return card;
    });
    if (!shown.length)
      groupList.replaceChildren(
        el(
          "p",
          groups.length
            ? "No groups match your search."
            : "No groups have been created for this workspace.",
        ),
      );
  }
  async function members(group, parent) {
    if (!group) {
      parent.replaceChildren();
      return;
    }
    const items = await all("groups.members", { groupId: group.id }, "after");
    stableList(parent, items, (m) => el("p", m.displayName || "Team member"));
    if (!items.length) parent.replaceChildren(el("p", "No members yet."));
  }
  function showLabs(group, parent) {
    const own = labs.filter(
      (l) =>
        group &&
        l.ownerKind === "team" &&
        l.ownerId === group.id &&
        l.phase !== "destroyed",
    );
    stableList(parent, own, (l) => {
      const holder = el("div");
      return labCard({ ...l, ownerName: group.name }, holder, {
        end: true,
        onchange: refresh,
      });
    });
    if (!own.length)
      parent.replaceChildren(
        el(
          "p",
          group
            ? "No active labs."
            : "Join a group to deploy and access its labs.",
        ),
      );
  }
  let manageGroup,
    manageInfo,
    manageMembers,
    manageLabs,
    manageFingerprint = "";
  async function refreshManaged() {
    if (!manageGroup) return;
    const g = groups.find((g) => g.id === manageGroup.value),
      sig = JSON.stringify(g);
    if (sig !== manageFingerprint) {
      manageFingerprint = sig;
      manageInfo.replaceChildren();
      if (g)
        manageInfo.append(
          el(
            "p",
            `${g.memberCount} members · ${g.enrollmentOpen ? "Enrollment open" : "Enrollment closed"}`,
          ),
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
        );
    }
    showLabs(g, manageLabs);
    await members(g, manageMembers);
  }
  if (admin) {
    const oversight = section("Manage project groups", panels.Manage);
    manageGroup = field(oversight, "Group", "select");
    manageInfo = el("div");
    oversight.append(manageInfo);
    manageMembers = el("div");
    section("Group members", panels.Manage).append(manageMembers);
    manageLabs = el("div", "", { class: "space-y-3" });
    section("Group labs", panels.Manage).append(manageLabs);
    manageGroup.onchange = () =>
      refreshManaged().catch((e) => message(e.message, true));
    const setup = section("Create project groups", panels.Manage),
      names = field(
        setup,
        "Group names — one per line (up to 100)",
        "textarea",
        { maxlength: 12900 },
      ),
      open = check(setup, "Open enrollment", true);
    setup.append(
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
          await mutate("groups.create", {
            names: list,
            enrollmentOpen: open.checked,
          });
          names.value = "";
          await refresh();
          message("Groups created.");
        },
        "primary",
      ),
    );
  }
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
        if (joined)
          currentInfo.append(
            el("h3", joined.name),
            el("p", `${joined.memberCount} members`),
            button("Leave group", () => membership(joined)),
          );
        else
          currentInfo.append(
            el("p", "Join a group to deploy and access its labs."),
            button("Find a group", () => views.select("Groups"), "primary"),
          );
      }
      deploy.hidden = !joined;
      roster.hidden = !joined;
      showLabs(joined, labList);
      renderGroups();
      await members(joined, memberList);
      if (manageGroup) {
        const old = manageGroup.value;
        options(manageGroup, groups);
        manageGroup.value = old || groups[0]?.id || "";
        await refreshManaged();
      }
    } finally {
      pending = false;
    }
  }
  discovery.append(button("Refresh groups", refresh));
  await refresh();
  poll(refresh);
});
