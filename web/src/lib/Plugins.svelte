<script lang="ts">
  import { page } from "$app/state";
  import GatewayClients from "./GatewayClients.svelte";
  import InstalledExtensions from "./InstalledExtensions.svelte";
  import Modal from "./Modal.svelte";
  import TargetSettings from "./TargetSettings.svelte";
  import Notifications from "./Notifications.svelte";
  import { onMount } from "svelte";
  import {
    Plug,
    Server,
    KeyRound,
    RefreshCw,
    ShieldCheck,
    Plus,
  } from "@lucide/svelte";
  import {
    api,
    collection,
    cachedCollection,
    type ProviderTarget,
    type Project,
  } from "./api";
  import type { components } from "./api.generated";
  import StatusBadge from "./StatusBadge.svelte";
  const sections = [
    { id: "environments", label: "Environments", icon: Server },
    { id: "plugins", label: "Plugins", icon: Plug },
    { id: "clients", label: "API clients", icon: KeyRound },
  ];
  let section = $derived(
    sections.find((s) => s.id === page.url.searchParams.get("section"))?.id ??
      "environments",
  );
  function tabKey(event: KeyboardEvent, index: number) {
    let next = index;
    if (event.key === "ArrowRight") next = (index + 1) % sections.length;
    else if (event.key === "ArrowLeft")
      next = (index + sections.length - 1) % sections.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = sections.length - 1;
    else return;
    event.preventDefault();
    const link = document.getElementById("integration-" + sections[next].id);
    link?.focus();
    link?.click();
  }
  type Grant = components["schemas"]["TargetGrantRecord"];
  let {
    projects,
    authMode,
    onchange,
  }: { projects: Project[]; authMode: string; onchange: () => Promise<void> } =
    $props();
  let targets = $state<ProviderTarget[]>(
    cachedCollection<ProviderTarget>("/admin/provider-targets") ?? [],
  );
  let grants = $state<Grant[]>([]);
  let projectId = $state(""),
    targetId = $state("");
  let busy = $state(false),
    loaded = $state(false),
    error = $state(""),
    notice = $state("");
  let settingsOpen = $state(false);
  let selectedTarget = $state<ProviderTarget>();
  let revoking = $state<Grant>();
  let dialog = $state<HTMLDialogElement>();
  onMount(() => {
    void run(load);
  });
  const valid = (value?: string) =>
    !value || value.startsWith("0001-") ? undefined : new Date(value).getTime();
  const current = (grant: Grant) =>
    (valid(grant.validFrom) ?? 0) <= Date.now() &&
    (valid(grant.validUntil) ?? Infinity) > Date.now();
  async function load() {
    const [nextTargets, nextGrants] = await Promise.all([
      collection<ProviderTarget>("/admin/provider-targets", "cursor", {
        fresh: true,
      }),
      authMode === "none"
        ? Promise.resolve([])
        : collection<Grant>("/provider-target-grants", "after", {
            fresh: true,
          }),
    ]);
    targets = nextTargets;
    grants = nextGrants;
    projectId ||= projects[0]?.id ?? "";
    targetId ||= targets[0]?.id ?? "";
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
      error =
        e instanceof Error
          ? e.message
          : "Plugin information could not be updated.";
    } finally {
      busy = false;
    }
  }
  async function refresh(target: ProviderTarget) {
    const updated = await api<ProviderTarget>(
      `/provider-targets/${encodeURIComponent(target.id)}/actions/refresh`,
      { method: "POST" },
    );
    targets = targets.map((item) => (item.id === updated.id ? updated : item));
    notice = "Environment discovery refreshed.";
  }
  async function grant() {
    const existing = grants.find(
      (g) =>
        g.scopeKind === "project" &&
        g.scopeId === projectId &&
        g.targetId === targetId,
    );
    if (existing && current(existing)) {
      notice = "This workspace already has access to that environment.";
      return;
    }
    if (existing && (valid(existing.validFrom) ?? 0) > Date.now()) {
      notice =
        "This workspace has scheduled access. Revoke that grant before changing its schedule.";
      return;
    }
    await api<Grant>(
      existing
        ? `/provider-target-grants/${encodeURIComponent(existing.id)}`
        : "/provider-target-grants",
      {
        method: existing ? "PUT" : "POST",
        headers: existing
          ? { "If-Match": `"${existing.resourceVersion}"` }
          : {},
        body: JSON.stringify({
          id: existing?.id ?? `grant.${crypto.randomUUID()}`,
          scopeKind: "project",
          scopeId: projectId,
          targetId,
          validFrom: new Date().toISOString(),
        }),
      },
    );
    await load();
    await onchange();
    notice =
      "Environment access granted to this workspace.";
  }
  async function revoke() {
    if (!revoking) return;
    await api(
      `/provider-target-grants/${encodeURIComponent(revoking.id)}/actions/revoke`,
      {
        method: "POST",
        headers: { "If-Match": `"${revoking.resourceVersion}"` },
      },
    );
    dialog?.close();
    revoking = undefined;
    await load();
    await onchange();
    notice =
      "Access grant revoked. Other inherited grants may still allow access.";
  }
  const scopeName = (grant: Grant) =>
    projects.find((p) => p.id === grant.scopeId)?.name ?? grant.scopeId;
</script>

<p class="mb-5 max-w-2xl text-sm text-base-content/70">
  Manage lab infrastructure, installed plugins and external applications.
</p>
<div
  role="tablist"
  aria-label="Integration categories"
  class="tabs tabs-border mb-6"
>
  {#each sections as item, index}<a
      id={"integration-" + item.id}
      href={"/plugins/?section=" + item.id}
      role="tab"
      class="tab gap-2 px-3 text-sm sm:px-4"
      class:tab-active={section === item.id}
      aria-selected={section === item.id}
      aria-controls={"integration-panel-" + item.id}
      tabindex={section === item.id ? 0 : -1}
      onkeydown={(event) => tabKey(event, index)}
      ><span class="hidden sm:inline-flex"><item.icon size={16} /></span
      >{item.label}</a
    >{/each}
</div>
<Notifications bind:error bind:notice />
<div
  role="tabpanel"
  id={"integration-panel-" + section}
  aria-labelledby={"integration-" + section}
  tabindex="0"
>
  {#if section === "plugins"}<InstalledExtensions />
  {:else if section === "clients"}<GatewayClients
      enabled={authMode !== "none"}
    />
  {:else}
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-base-content/70">
        Choose which workspaces can deploy to each environment.
      </p>
      <button class="btn btn-sm" disabled={busy} onclick={() => run(load)}
        ><RefreshCw size={16} />Refresh list</button
      >
    </div>
    <section>
      <h2 class="mb-4 flex items-center gap-2 text-lg font-semibold">
        <Server size={20} />Environments
      </h2>
      <div class="grid gap-4 md:grid-cols-2">
        {#each targets as target (target.id)}<article
            class="min-w-0 rounded-box border border-base-300 p-5"
          >
            <div class="flex flex-wrap items-center justify-between gap-3">
              <h3 class="font-semibold capitalize">{target.providerKind}</h3>
              <StatusBadge state={target.status} />
            </div>
            <p class="mt-2 break-all text-sm">{target.id}</p>
            <dl class="my-4 grid gap-2 text-sm text-base-content/70">
              <div>
                <dt class="inline font-medium">Protocol:</dt>
                <dd class="inline">
                  {target.protocolMajor}.{target.protocolMinor}
                </dd>
              </div>
              <div>
                <dt class="inline font-medium">Last discovered:</dt>
                <dd class="inline">
                  {target.discoveredAt
                    ? new Date(target.discoveredAt).toLocaleString()
                    : "Not yet discovered"}
                </dd>
              </div>
              <div>
                <dt class="inline font-medium">Discovery valid until:</dt>
                <dd class="inline">
                  {target.validUntil
                    ? new Date(target.validUntil).toLocaleString()
                    : "Unavailable"}
                </dd>
              </div>
            </dl>
            <button
              class="btn btn-sm"
              disabled={busy}
              onclick={() => run(() => refresh(target))}
              ><RefreshCw size={16} />Refresh {target.id}</button
            >
            <button
              class="btn btn-sm ml-2"
              onclick={() => {
                selectedTarget = target;
                settingsOpen = true;
              }}>Settings & limits</button
            >
          </article>{/each}
      </div>
      {#if loaded && !targets.length}<p class="py-6 text-base-content/70">
          No provider environments are registered. Install a provider to connect
          infrastructure.
        </p>{/if}
    </section>
    {#if authMode !== "none"}<section class="mt-8 min-w-0">
        <h2 class="mb-4 flex items-center gap-2 text-lg font-semibold">
          <ShieldCheck size={20} />Workspace environment access
        </h2>
        <form
          class="mb-5 flex flex-wrap items-end gap-3"
          onsubmit={(e) => {
            e.preventDefault();
            void run(grant);
          }}
        >
          <label class="grid w-full gap-2 text-sm sm:w-64"
            >Workspace<select
              aria-label="Workspace"
              class="select w-full"
              bind:value={projectId}
              required
              disabled={busy}
              ><option value="" disabled>Select workspace</option
              >{#each projects as project}<option value={project.id}
                  >{project.name}</option
                >{/each}</select
            ></label
          >
          <label class="grid w-full gap-2 text-sm sm:w-64"
            >Environment<select
              aria-label="Environment"
              class="select w-full"
              bind:value={targetId}
              required
              disabled={busy}
              ><option value="" disabled>Select environment</option
              >{#each targets as target}<option value={target.id}
                  >{target.id}</option
                >{/each}</select
            ></label
          >
          <button
            class="btn"
            disabled={busy || !loaded || !projectId || !targetId}
            ><Plus size={16} />Grant access</button
          >
        </form>
        <div
          class="max-w-full overflow-x-auto rounded-box border border-base-300"
        >
          <table class="table table-sm">
            <thead
              ><tr
                ><th>Scope</th><th>Environment</th><th>Access</th><th>Action</th
                ></tr
              ></thead
            >
            <tbody
              >{#each grants as item (item.id)}<tr
                  ><td
                    >{scopeName(item)}
                    <p class="text-xs text-base-content/60">
                      {item.scopeKind}
                    </p></td
                  ><td>{item.targetId}</td><td
                    >{current(item)
                      ? "Active"
                      : (valid(item.validFrom) ?? 0) > Date.now()
                        ? "Scheduled"
                        : "Expired or revoked"}</td
                  ><td
                    >{#if (valid(item.validUntil) ?? Infinity) > Date.now()}<button
                        class="btn btn-sm btn-ghost"
                        disabled={busy}
                        onclick={() => {
                          revoking = item;
                          dialog?.showModal();
                        }}>Revoke access to {scopeName(item)}</button
                      >{/if}</td
                  ></tr
                >{/each}</tbody
            >
          </table>
        </div>
        {#if loaded && !grants.length}<p
            class="py-5 text-sm text-base-content/70"
          >
            No environment access has been granted yet.
          </p>{/if}
        <p class="mt-3 text-sm text-base-content/70">
          Organization grants apply to their workspaces. People also need an
          appropriate workspace role.
        </p>
      </section>{/if}
    <dialog
      bind:this={dialog}
      class="modal"
      aria-labelledby="revoke-grant-title"
    >
      <div class="modal-box">
        <h2 id="revoke-grant-title" class="text-lg font-semibold">
          Revoke environment access?
        </h2>
        <p class="mt-4 break-words">
          Remove the {revoking?.scopeKind} grant for {revoking
            ? scopeName(revoking)
            : ""} on {revoking?.targetId}? This can block new launches and
          console access. Existing labs are not deleted.
        </p>
        <div class="modal-action">
          <form method="dialog">
            <button class="btn" disabled={busy}>Cancel</button>
          </form>
          <button
            class="btn btn-error"
            disabled={busy}
            onclick={() => run(revoke)}>Revoke grant</button
          >
        </div>
        {#if error}<p role="alert" class="mt-3 text-error">{error}</p>{/if}
      </div>
    </dialog>

    <Modal bind:open={settingsOpen} title="Environment settings"
      >{#if selectedTarget}{#key selectedTarget.id}<TargetSettings
            target={selectedTarget}
          />{/key}{/if}</Modal
    >
  {/if}
</div>
