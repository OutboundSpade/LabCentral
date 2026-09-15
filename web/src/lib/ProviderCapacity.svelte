<script lang="ts">
  import { onMount } from "svelte";
  import {
    api,
    ApiError,
    collection,
    type ProviderTarget,
    type Quota,
  } from "./api";
  import { discoveryJSON } from "./schema";
  import Modal from "./Modal.svelte";
  import TargetSettings from "./TargetSettings.svelte";
  type Usage = {
    dimension: string;
    reserved: number;
    committed: number;
    retained: number;
    total: number;
  };
  type Node = {
    name: string;
    status: string;
    maxCPU?: number;
    cpuUsage?: number;
    maxMemoryBytes?: number;
    usedMemoryBytes?: number;
  };
  type Storage = {
    node: string;
    id: string;
    totalBytes?: number;
    usedBytes?: number;
    availableBytes?: number;
    shared?: boolean;
    active?: boolean;
  };
  type Capacity = {
    formatVersion: number;
    nodes?: Node[];
    storages?: Storage[];
  };
  type Row = {
    target: ProviderTarget;
    capacity?: Capacity;
    quota?: Quota;
    usage?: Usage;
    maximum?: number;
    error?: string;
  };
  let rows = $state<Row[]>([]),
    busy = $state(false),
    error = $state("");
  let selected = $state<ProviderTarget>();
  let settingsOpen = $state(false);
  const bytes = (n?: number) =>
    typeof n === "number" && Number.isFinite(n)
      ? `${(n / 1073741824).toFixed(1)} GiB`
      : "Unavailable";
  const percent = (used?: number, total?: number) =>
    typeof used === "number" && total && total > 0
      ? Math.min(100, Math.max(0, (100 * used) / total))
      : undefined;
  onMount(() => {
    void load();
  });
  async function row(target: ProviderTarget): Promise<Row> {
    const result: Row = { target };
    const capacity = discoveryJSON(target.discovery, "capacity_json");
    if (target.providerKind === "proxmox" && capacity?.formatVersion === 1)
      result.capacity = capacity;
    const outcomes = await Promise.allSettled([
      api<Quota>(`/quotas/target/${encodeURIComponent(target.id)}/vms`, {
        fresh: true,
      }).catch((e) => {
        if (e instanceof ApiError && e.status === 404) return undefined;
        throw e;
      }),
      api<{ items: Usage[] }>(
        `/quota-usage/target/${encodeURIComponent(target.id)}`,
        { fresh: true },
      ),
      api<{ maxConcurrentDeployments: number }>(
        `/admin/provider-targets/${encodeURIComponent(target.id)}/deployment-settings`,
        { fresh: true },
      ),
    ]);
    if (outcomes[0].status === "fulfilled") result.quota = outcomes[0].value;
    if (outcomes[1].status === "fulfilled")
      result.usage = outcomes[1].value.items?.find(
        (u) => u.dimension === "vms",
      );
    if (outcomes[2].status === "fulfilled")
      result.maximum = outcomes[2].value.maxConcurrentDeployments;
    if (outcomes.some((o) => o.status === "rejected"))
      result.error = "Some limits or usage could not be loaded.";
    return result;
  }
  async function load() {
    if (busy) return;
    busy = true;
    error = "";
    try {
      const targets = await collection<ProviderTarget>(
        "/admin/provider-targets",
        "cursor",
        { fresh: true },
      );
      const next: Row[] = [];
      for (const target of targets) next.push(await row(target));
      rows = next;
    } catch (e) {
      error = e instanceof Error ? e.message : "Capacity unavailable.";
    } finally {
      busy = false;
    }
  }
  async function refresh(target: ProviderTarget) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      const updated = await api<ProviderTarget>(
        `/provider-targets/${encodeURIComponent(target.id)}/actions/refresh`,
        { method: "POST" },
      );
      const next = await row(updated);
      rows = rows.map((r) => (r.target.id === target.id ? next : r));
    } catch (e) {
      error = e instanceof Error ? e.message : "Discovery could not refresh.";
    } finally {
      busy = false;
    }
  }
</script>

<section class="mb-8">
  <div class="mb-4 flex items-center justify-between gap-3">
    <h2 class="text-lg font-semibold">Environment capacity & limits</h2>
    <button class="btn btn-sm" disabled={busy} onclick={load}
      >Reload capacity</button
    >
  </div>
  <p class="mb-4 text-sm text-base-content/60">
    Provider capacity is a discovery snapshot. Quotas count LabCentral
    reservations and resources across all workspaces; they do not include
    unrelated provider workloads.
  </p>
  {#if error}<p role="alert" class="mb-4 text-sm text-error">{error}</p>{/if}
  <div class="grid gap-4 xl:grid-cols-2">
    {#each rows as r (r.target.id)}<article
        class="min-w-0 rounded-box border border-base-300 p-5"
      >
        <header class="mb-4 flex flex-wrap items-center gap-3">
          <h3 class="mr-auto break-all font-semibold">{r.target.id}</h3>
          <button
            class="btn btn-sm"
            disabled={busy}
            onclick={() => refresh(r.target)}>Refresh capacity</button
          ><button
            class="btn btn-sm"
            onclick={() => {
              selected = r.target;
              settingsOpen = true;
            }}>Configure limits</button
          >
        </header>
        <dl class="mb-4 grid grid-cols-2 gap-4 text-sm">
          <div>
            <dt class="text-base-content/60">Concurrent deployments</dt>
            <dd class="mt-1 text-xl font-semibold">
              {r.maximum ?? "Unavailable"}
            </dd>
          </div>
          <div>
            <dt class="text-base-content/60">VM quota used / limit</dt>
            <dd class="mt-1 text-xl font-semibold">
              {r.usage?.total ?? "Unavailable"} / {r.error && !r.quota
                ? "Unavailable"
                : (r.quota?.limit ?? "Not configured")}
            </dd>
          </div>
        </dl>
        {#if r.usage}<p class="mb-4 text-xs text-base-content/60">
            {r.usage.reserved} reserved · {r.usage.committed} committed · {r
              .usage.retained} awaiting cleanup
          </p>{/if}
        {#if r.error}<p class="mb-3 text-sm text-warning">{r.error}</p>{/if}
        {#if r.capacity?.nodes?.length}{#each r.capacity.nodes as node}<div
              class="mb-4 grid gap-2 border-t border-base-300 pt-4"
            >
              <h4 class="text-sm font-semibold">{node.name} · {node.status}</h4>
              <p class="text-sm">
                CPU: {node.maxCPU ?? "Unavailable"} cores · {typeof node.cpuUsage ===
                "number"
                  ? `${(node.cpuUsage * 100).toFixed(1)}% used`
                  : "usage unavailable"}
              </p>
              <p class="text-sm">
                Memory: {bytes(node.usedMemoryBytes)} / {bytes(
                  node.maxMemoryBytes,
                )}
              </p>
              {#if percent(node.usedMemoryBytes, node.maxMemoryBytes) !== undefined}<progress
                  class="progress progress-primary w-full"
                  aria-label={`${node.name} memory used`}
                  value={percent(node.usedMemoryBytes, node.maxMemoryBytes)}
                  max="100"
                ></progress>{/if}
            </div>{/each}
          {#if r.capacity.storages?.length}<h4
              class="mt-4 text-sm font-semibold"
            >
              Storage
            </h4>
            <div class="mt-2 space-y-3">
              {#each r.capacity.storages as disk}<div class="text-sm">
                  <p>
                    {disk.node} / {disk.id}{disk.shared
                      ? " (shared)"
                      : ""}{disk.active === false ? " · inactive" : ""}
                  </p>
                  <p class="text-xs text-base-content/60">
                    {bytes(disk.availableBytes)} available · {bytes(
                      disk.usedBytes,
                    )} used / {bytes(disk.totalBytes)}
                  </p>
                </div>{/each}
            </div>{/if}
        {:else}<p class="text-sm text-base-content/60">
            This environment has no supported physical capacity report. Use its
            VM quota to set an admission limit.
          </p>{/if}
        <p class="mt-4 text-xs text-base-content/60">
          Discovered {r.target.discoveredAt
            ? new Date(r.target.discoveredAt).toLocaleString()
            : "never"}{r.target.validUntil &&
          new Date(r.target.validUntil).getTime() < Date.now()
            ? " · snapshot expired"
            : ""}
        </p>
      </article>{/each}
  </div>
</section>
<Modal bind:open={settingsOpen} title="Environment limits"
  >{#if selected}{#key selected.id}<TargetSettings
        target={selected}
      />{/key}{/if}</Modal
>
