<script lang="ts">
  import Notifications from "./Notifications.svelte";
  import { onMount } from "svelte";
  import { Server, Clock, CircleAlert } from "@lucide/svelte";
  import {
    api,
    cached,
    ApiError,
    mutationHeaders,
    type ProviderTarget,
    type Quota,
    type LifetimePolicy,
  } from "./api";
  import StatusBadge from "./StatusBadge.svelte";
  let {
    projectId,
    organizationId,
    targets,
  }: {
    projectId: string;
    organizationId: string;
    targets: ProviderTarget[];
  } = $props();
  const quotaPath = $derived(
    `/quotas/project/${encodeURIComponent(projectId)}/instances?organizationId=${encodeURIComponent(organizationId)}`,
  );
  const lifetimePath = $derived(
    `/lifetime-policies/project/${encodeURIComponent(projectId)}?organizationId=${encodeURIComponent(organizationId)}`,
  );
  let quota = $state<Quota>();
  let policy = $state<LifetimePolicy>();
  let limit = $state(25);
  let hours = $state(4);
  let loaded = $state(false);
  let busy = $state(false);
  let error = $state("");
  let notice = $state("");
  onMount(() => {
    quota = cached<Quota>(quotaPath);
    policy = cached<LifetimePolicy>(lifetimePath);
    limit = quota?.limit ?? 25;
    hours = policy ? policy.defaultTtlSeconds / 3600 : 4;
    void load();
  });
  async function optional<T>(path: string): Promise<T | undefined> {
    try {
      return await api<T>(path);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return undefined;
      throw e;
    }
  }
  async function load() {
    loaded = false;
    try {
      [quota, policy] = await Promise.all([
        optional<Quota>(quotaPath),
        optional<LifetimePolicy>(lifetimePath),
      ]);
      limit = quota?.limit ?? 25;
      hours = policy ? policy.defaultTtlSeconds / 3600 : 4;
      loaded = true;
    } catch (e) {
      error = e instanceof Error ? e.message : "Settings could not load.";
    }
  }
  async function save() {
    if (busy || !loaded) return;
    busy = true;
    error = "";
    notice = "";
    try {
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
      policy = await api<LifetimePolicy>(lifetimePath, {
        method: "PUT",
        headers: {
          ...mutationHeaders(),
          ...(policy
            ? { "If-Match": `"${policy.resourceVersion}"` }
            : { "If-None-Match": "*" }),
        },
        body: JSON.stringify({
          defaultTtlSeconds: hours * 3600,
          maximumTtlSeconds: Math.max(
            hours * 3600,
            policy?.maximumTtlSeconds ?? 14400,
          ),
          renewalWindowSeconds: policy?.renewalWindowSeconds ?? 0,
          warningBeforeSeconds: policy?.warningBeforeSeconds ?? 300,
          destructionGraceSeconds: policy?.destructionGraceSeconds ?? 0,
          renewalAllowed: policy?.renewalAllowed ?? false,
          persistentAllowed: policy?.persistentAllowed ?? false,
        }),
      });
      notice = "Project limits saved. Existing lab expiry times are unchanged.";
    } catch (e) {
      error = e instanceof Error ? e.message : "Settings could not be saved.";
    } finally {
      busy = false;
    }
  }
</script>

<Notifications bind:error bind:notice />
<form
  class="grid max-w-2xl gap-5 rounded-box border border-base-300 p-6"
  onsubmit={(e) => {
    e.preventDefault();
    void save();
  }}
>
  <h2 class="flex items-center gap-2 text-lg font-semibold">
    <Clock size={20} />Lab limits
  </h2>
  <label class="grid gap-2 text-sm"
    >Maximum active labs in this project<input
      aria-label="Maximum active labs in this project"
      class="input w-full"
      type="number"
      min="0"
      max="100"
      step="1"
      bind:value={limit}
      required
    /></label
  ><label class="grid gap-2 text-sm"
    >Default lab lifetime (hours)<input
      aria-label="Default lab lifetime (hours)"
      class="input w-full"
      type="number"
      min="1"
      max="24"
      step="1"
      bind:value={hours}
      required
    /></label
  ><button class="btn justify-self-start" disabled={busy || !loaded}
    >Save limits</button
  >
</form>
<section class="mt-8">
  <h2 class="mb-4 flex items-center gap-2 text-lg font-semibold">
    <Server size={20} />Environments
  </h2>
  <div class="grid gap-4 md:grid-cols-2">
    {#each targets as target}<article
        class="rounded-box border border-base-300 p-5"
      >
        <div class="flex items-center justify-between gap-3">
          <h3 class="font-semibold capitalize">{target.providerKind}</h3>
          <StatusBadge state={target.status} />
        </div>

        <p class="mt-2 text-sm text-base-content/70">
          {target.discoveredAt
            ? `Last checked ${new Date(target.discoveredAt).toLocaleString()}`
            : "Waiting for discovery"}
        </p>
      </article>{/each}
  </div>
</section>
