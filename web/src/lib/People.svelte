<script lang="ts">
  import Notifications from "./Notifications.svelte";
  import { onMount } from "svelte";
  import { UserPlus, ShieldCheck, CircleAlert } from "@lucide/svelte";
  import { api, collection, cachedCollection, type Person, type Project } from "./api";
  let { project, authMode }: { project?: Project; authMode: string } = $props();
  let people = $state<Person[]>(cachedCollection<Person>("/users", "after") ?? []);
  let busy = $state(false);
  let error = $state("");
  let notice = $state("");
  let username = $state("");
  let displayName = $state("");
  let externalSubject = $state("");
  let password = $state("");
  let selectedPerson = $state("");
  let role = $state("participant");
  onMount(() => {
    void load();
  });
  async function load() {
    try {
      people = await collection<Person>("/users", "after");
    } catch (e) {
      error = e instanceof Error ? e.message : "People could not load.";
    }
  }
  async function run(work: () => Promise<void>) {
    if (busy) return;
    busy = true;
    error = "";
    notice = "";
    try {
      await work();
    } catch (e) {
      error = e instanceof Error ? e.message : "The change could not be saved.";
    } finally {
      busy = false;
    }
  }
  async function create() {
    await run(async () => {
      const person = await api<Person>("/users", {
        method: "POST",
        body: JSON.stringify({
          id: `user.${crypto.randomUUID()}`,
          username,
          displayName,
          ...(authMode === "oidc" ? { externalSubject } : { password }),
        }),
      });
      selectedPerson = person.id;
      username = "";
      displayName = "";
      externalSubject = "";
      password = "";
      await load();
      notice = "Account created. Assign project access below.";
    });
  }
  async function grant() {
    await run(async () => {
      if (!project || !selectedPerson) return;
      const memberships = await collection<{
        id: string;
        userId: string;
        teamId?: string;
        validUntil?: string;
      }>(
        `/memberships?organizationId=${encodeURIComponent(project.organizationId)}`,
        "after",
      );
      if (
        !memberships.some(
          (m) =>
            m.userId === selectedPerson &&
            !m.teamId &&
            (!m.validUntil || m.validUntil.startsWith("0001-") || new Date(m.validUntil).getTime() > Date.now()),
        )
      ) {
        await api("/memberships", {
          method: "POST",
          body: JSON.stringify({
            id: `membership.${crypto.randomUUID()}`,
            userId: selectedPerson,
            organizationId: project.organizationId,
          }),
        });
      }
      await api("/role-assignments", {
        method: "POST",
        body: JSON.stringify({
          id: `role.${crypto.randomUUID()}`,
          subjectKind: "user",
          subjectId: selectedPerson,
          scopeKind: "project",
          scopeId: project.id,
          role,
        }),
      });
      notice = "Project access assigned.";
    });
  }
  async function setEnabled(person: Person) {
    await run(async () => {
      await api(
        `/users/${person.id}/actions/${person.disabled ? "enable" : "disable"}`,
        {
          method: "POST",
          headers: { "If-Match": `"${person.resourceVersion}"` },
          body: "{}",
        },
      );
      await load();
      notice = person.disabled
        ? "Account enabled."
        : "Account disabled and sessions revoked.";
    });
  }
</script>

<Notifications bind:error bind:notice />
<div class="grid gap-6 xl:grid-cols-2">
  <form
    class="grid gap-4 rounded-box border border-base-300 p-6"
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <h2 class="flex items-center gap-2 text-lg font-semibold">
      <UserPlus size={20} />Add an account
    </h2>
    <label class="grid gap-2 text-sm"
      >Display name<input aria-label="Display name"
        class="input w-full"
        bind:value={displayName}
        required
      /></label
    ><label class="grid gap-2 text-sm"
      >Username<input aria-label="Username"
        class="input w-full"
        bind:value={username}
        pattern={"[a-z][a-z0-9-]{0,62}"}
        required
      /></label
    >{#if authMode === "oidc"}<label class="grid gap-2 text-sm"
        >Identity provider subject<input aria-label="Identity provider subject"
          class="input w-full font-mono text-sm"
          bind:value={externalSubject}
          required
        /></label
      >
      <p class="text-sm leading-6 text-base-content/70">
        Copy the user's exact subject from OIDC provider. Their passkey stays with
        the identity provider.
      </p>{:else}<label class="grid gap-2 text-sm"
        >Initial password<input aria-label="Initial password"
          class="input w-full"
          type="password"
          autocomplete="new-password"
          bind:value={password}
          minlength="14"
          required
        /></label
      >{/if}<button class="btn justify-self-start" disabled={busy}
      >Create account</button
    >
  </form>
  <form
    class="grid content-start gap-4 rounded-box border border-base-300 p-6"
    onsubmit={(e) => {
      e.preventDefault();
      void grant();
    }}
  >
    <h2 class="flex items-center gap-2 text-lg font-semibold">
      <ShieldCheck size={20} />Assign project access
    </h2>
    <p class="text-sm text-base-content/70">Project: {project?.name}</p>
    <label class="grid gap-2 text-sm"
      >Person<select aria-label="Person" class="select w-full" bind:value={selectedPerson} required
        ><option value="">Choose a person</option
        >{#each people.filter((p) => !p.disabled) as p}<option value={p.id}
            >{p.displayName}</option
          >{/each}</select
      ></label
    ><label class="grid gap-2 text-sm"
      >Role<select aria-label="Role" class="select w-full" bind:value={role}
        ><option value="participant">Participant</option><option value="instructor"
          >Instructor</option
        ><option value="lab-author">Lab author</option><option value="viewer"
          >Viewer</option
        ></select
      ></label
    ><button class="btn justify-self-start" disabled={busy || !selectedPerson}
      >Assign access</button
    >
  </form>
</div>
<div class="mt-7 max-w-full overflow-x-auto rounded-box border border-base-300">
  <table class="table table-sm">
    <thead
      ><tr><th>Name</th><th>Username</th><th>Status</th><th>Account</th></tr
      ></thead
    ><tbody
      >{#each people as person}<tr
          ><td class="font-medium">{person.displayName}</td><td
            >{person.username}</td
          ><td>{person.disabled ? "Disabled" : "Enabled"}</td><td
            ><button
              class="btn btn-sm btn-ghost"
              disabled={busy}
              onclick={() => setEnabled(person)}
              >{person.disabled ? "Enable" : "Disable"}</button
            ></td
          ></tr
        >{/each}</tbody
    >
  </table>
</div>
