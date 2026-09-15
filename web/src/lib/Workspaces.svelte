<script lang="ts">
  import Notifications from "./Notifications.svelte";
  import { onMount } from "svelte";
  import { FolderOpen, Plus, Pencil, RefreshCw } from "@lucide/svelte";
  import { api, collection, cachedCollection, type Project } from "./api";
  import type { components } from "./api.generated";
  type Organization = components["schemas"]["OrganizationRecord"];
  let {
    projects,
    onchange,
    onselect,
  }: {
    projects: Project[];
    onchange: () => Promise<void>;
    onselect: (id: string) => Promise<void>;
  } = $props();
  let organizations = $state<Organization[]>(
    cachedCollection<Organization>("/organizations", "after") ?? [],
  );
  let organizationId = $state("");
  let name = $state("");
  let organizationName = $state("");
  let editing = $state<Project>();
  let editingOrganization = $state<Organization>();
  let busy = $state(false),
    loaded = $state(false),
    error = $state(""),
    notice = $state("");
  onMount(() => {
    void run(load);
  });
  async function load() {
    organizations = await collection<Organization>("/organizations", "after", {
      fresh: true,
    });
    organizationId ||= organizations[0]?.id ?? "";
    loaded = true;
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
  async function saveWorkspace() {
    const record = await api<Project>(
      editing ? `/projects/${encodeURIComponent(editing.id)}` : "/projects",
      {
        method: editing ? "PUT" : "POST",
        headers: editing ? { "If-Match": `"${editing.resourceVersion}"` } : {},
        body: JSON.stringify({
          id: editing?.id ?? `project.${crypto.randomUUID()}`,
          organizationId: editing?.organizationId ?? organizationId,
          name: name.trim(),
        }),
      },
    );
    editing = undefined;
    name = "";
    await onchange();
    notice = `Workspace “${record.name}” saved. Configure its people, environment access and limits before launching labs.`;
  }
  async function saveOrganization() {
    const record = await api<Organization>(
      editingOrganization
        ? `/organizations/${encodeURIComponent(editingOrganization.id)}`
        : "/organizations",
      {
        method: editingOrganization ? "PUT" : "POST",
        headers: editingOrganization
          ? { "If-Match": `"${editingOrganization.resourceVersion}"` }
          : {},
        body: JSON.stringify({
          id: editingOrganization?.id ?? `organization.${crypto.randomUUID()}`,
          name: organizationName.trim(),
        }),
      },
    );
    editingOrganization = undefined;
    organizationName = "";
    organizationId = record.id;
    await load();
    notice = "Organization saved.";
  }
</script>

<div class="mb-6 flex flex-wrap items-center gap-3">
  <p class="max-w-2xl text-sm text-base-content/70">
    Workspaces group lab templates, people and limits within an organization.
  </p>
  <button
    class="btn btn-sm ml-auto"
    disabled={busy}
    onclick={() =>
      run(async () => {
        await load();
        await onchange();
      })}><RefreshCw size={16} />Refresh</button
  >
</div>
<Notifications bind:error bind:notice />
<div class="grid gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
  <section class="min-w-0">
    <h2 class="mb-4 text-lg font-semibold">Your workspaces</h2>
    <div class="max-w-full overflow-x-auto rounded-box border border-base-300">
      <table class="table table-sm">
        <thead
          ><tr
            ><th>Workspace</th><th>Organization</th><th>Classroom launches</th
            ><th>Actions</th></tr
          ></thead
        >
        <tbody
          >{#each projects as project (project.id)}<tr>
              <td class="font-medium">{project.name}</td>
              <td
                >{organizations.find((o) => o.id === project.organizationId)
                  ?.name ?? project.organizationId}</td
              >
              <td
                >{project.allowedActions?.includes("instance.batchCreate")
                  ? "Enabled for you"
                  : "Needs role or environment access"}</td
              >
              <td
                ><div class="flex gap-2">
                  <a
                    class="btn btn-sm"
                    href="/settings/"
                    onclick={() => onselect(project.id)}>Open {project.name}</a
                  ><button
                    class="btn btn-sm btn-ghost"
                    aria-label={`Rename ${project.name}`}
                    onclick={() => {
                      editing = project;
                      name = project.name;
                      organizationId = project.organizationId;
                    }}><Pencil size={16} /></button
                  >
                </div></td
              >
            </tr>{/each}</tbody
        >
      </table>
    </div>
    {#if !projects.length}<div class="py-8 text-base-content/70">
        <FolderOpen size={24} class="mb-3" />
        <p>Create your first workspace to start organizing labs.</p>
      </div>{/if}
    <h2 class="mb-4 mt-8 text-lg font-semibold">Organizations</h2>
    <ul class="divide-y divide-base-300">
      {#each organizations as organization}<li
          class="flex items-center justify-between gap-3 py-3"
        >
          <span>{organization.name}</span><button
            class="btn btn-sm btn-ghost"
            aria-label={`Rename organization ${organization.name}`}
            onclick={() => {
              editingOrganization = organization;
              organizationName = organization.name;
            }}><Pencil size={16} /></button
          >
        </li>{/each}
    </ul>
  </section>
  <div class="min-w-0 space-y-6">
    <form
      class="grid gap-4 rounded-box border border-base-300 p-5"
      onsubmit={(e) => {
        e.preventDefault();
        void run(saveWorkspace);
      }}
    >
      <h2 class="flex items-center gap-2 font-semibold">
        <FolderOpen size={18} />{editing
          ? "Rename workspace"
          : "Create workspace"}
      </h2>
      <label class="grid gap-2 text-sm"
        >Workspace name<input aria-label="Workspace name"
          class="input w-full"
          bind:value={name}
          required
          maxlength="128"
        /></label
      >
      <label class="grid gap-2 text-sm"
        >Organization<select aria-label="Organization"
          class="select w-full"
          bind:value={organizationId}
          required
          disabled={!!editing || busy}
          ><option value="" disabled>Select an organization</option
          >{#each organizations as org}<option value={org.id}>{org.name}</option
            >{/each}</select
        ></label
      >
      <div class="flex flex-wrap gap-2">
        <button
          class="btn btn-primary"
          disabled={busy || !loaded || !organizationId || !name.trim()}
          >{editing ? "Save workspace" : "Create workspace"}</button
        >{#if editing}<button
            type="button"
            class="btn btn-ghost"
            onclick={() => {
              editing = undefined;
              name = "";
            }}>Cancel</button
          >{/if}
      </div>
    </form>
    <form
      class="grid gap-4 rounded-box border border-base-300 p-5"
      onsubmit={(e) => {
        e.preventDefault();
        void run(saveOrganization);
      }}
    >
      <h2 class="flex items-center gap-2 font-semibold">
        <Plus size={18} />{editingOrganization
          ? "Rename organization"
          : "Create organization"}
      </h2>
      <label class="grid gap-2 text-sm"
        >Organization name<input aria-label="Organization name"
          class="input w-full"
          bind:value={organizationName}
          required
          maxlength="128"
        /></label
      >
      <div class="flex flex-wrap gap-2">
        <button
          class="btn"
          disabled={busy || !loaded || !organizationName.trim()}
          >{editingOrganization
            ? "Save organization"
            : "Create organization"}</button
        >{#if editingOrganization}<button
            type="button"
            class="btn btn-ghost"
            onclick={() => {
              editingOrganization = undefined;
              organizationName = "";
            }}>Cancel</button
          >{/if}
      </div>
    </form>
    <a href="/plugins/" class="link text-sm">Manage environment access</a>
  </div>
</div>
