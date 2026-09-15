<script lang="ts">
  import { onMount } from "svelte";
  import {
    api,
    ApiError,
    mutationHeaders,
    type ProviderTarget,
    type Quota,
  } from "./api";
  let { target }: { target: ProviderTarget } = $props();
  type Deployment = {
    maxConcurrentDeployments: number;
    resourceVersion: number;
  };
  let deployment = $state<Deployment>();
  let quota = $state<Quota>();
  let concurrency = $state<number | undefined>();
  let limit = $state<number | undefined>();
  let loaded = $state(false),
    busy = $state(false),
    error = $state(""),
    notice = $state("");
  const deploymentPath = $derived(
    `/admin/provider-targets/${encodeURIComponent(target.id)}/deployment-settings`,
  );
  const quotaPath = $derived(
    `/quotas/target/${encodeURIComponent(target.id)}/vms`,
  );
  async function load() {
    loaded = false;
    const [settings, nextQuota] = await Promise.all([
      api<Deployment>(deploymentPath, { fresh: true }),
      api<Quota>(quotaPath, { fresh: true }).catch((e) => {
        if (e instanceof ApiError && e.status === 404) return undefined;
        throw e;
      }),
    ]);
    deployment = settings;
    quota = nextQuota;
    concurrency = settings.maxConcurrentDeployments;
    limit = quota?.limit;
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
      error = e instanceof Error ? e.message : "Settings could not be updated.";
    } finally {
      busy = false;
    }
  }
  onMount(() => {
    void run(load);
  });
  async function saveConcurrency() {
    if (!deployment || !concurrency) return;
    deployment = await api<Deployment>(deploymentPath, {
      method: "PUT",
      headers: { "If-Match": `"${deployment.resourceVersion}"` },
      body: JSON.stringify({ maxConcurrentDeployments: concurrency }),
    });
    notice =
      "Provisioning limit saved. It applies to new admissions immediately; running work keeps its slots.";
  }
  async function saveQuota() {
    if (limit === undefined) return;
    quota = await api<Quota>(quotaPath, {
      method: "PUT",
      headers: {
        ...mutationHeaders(),
        ...(quota
          ? { "If-Match": `"${quota.resourceVersion}"` }
          : { "If-None-Match": "*" }),
      },
      body: JSON.stringify({ limit }),
    });
    notice =
      "VM quota saved. It applies across all workspaces using this environment.";
  }
</script>

<p class="mb-5 break-all text-sm text-base-content/60">
  {target.providerKind} · {target.id}
</p>
{#if error}<p role="alert" class="mb-4 text-sm text-error">
    {error}
    <button class="btn btn-xs" disabled={busy} onclick={() => run(load)}
      >Reload settings</button
    >
  </p>{/if}
{#if notice}<p role="status" class="mb-4 text-sm">{notice}</p>{/if}
<form
  class="grid gap-3 border-b border-base-300 pb-6"
  onsubmit={(e) => {
    e.preventDefault();
    void run(saveConcurrency);
  }}
>
  <label class="grid gap-2 text-sm font-medium"
    >Concurrent deployments<input
      class="input w-full"
      type="number"
      min="1"
      max="16"
      step="1"
      required
      bind:value={concurrency}
      disabled={!loaded || busy}
    /></label
  >
  <p class="text-sm text-base-content/60">
    Maximum labs provisioning or cleaning up at once on this environment (1–16).
    Independent labs run in parallel; resources inside each lab follow their
    dependency order.
  </p>
  <button class="btn justify-self-start" disabled={!loaded || busy}
    >Save provisioning limit</button
  >
</form>
<form
  class="mt-6 grid gap-3"
  onsubmit={(e) => {
    e.preventDefault();
    void run(saveQuota);
  }}
>
  <label class="grid gap-2 text-sm font-medium"
    >VM count limit<input
      class="input w-full"
      type="number"
      min="0"
      max="9007199254740991"
      step="1"
      required
      bind:value={limit}
      placeholder="No target VM quota configured"
      disabled={!loaded || busy}
    /></label
  >
  <p class="text-sm text-base-content/60">
    LabCentral quota, including reservations and resources awaiting cleanup.
    Zero blocks new VMs. Provider account limits and physical capacity may
    impose a lower ceiling. Existing VMs are not deleted when this is reduced.
  </p>
  <button
    class="btn justify-self-start"
    disabled={!loaded || busy || limit === undefined}>Save VM limit</button
  >
</form>
<section class="mt-6 border-t border-base-300 pt-5">
  <h3 class="mb-2 text-sm font-semibold">Protocol features</h3>
  <ul class="space-y-1 break-all text-xs text-base-content/60">
    {#each target.features ?? [] as feature}<li>{feature}</li>{:else}<li>
        No optional features reported.
      </li>{/each}
  </ul>
</section>
