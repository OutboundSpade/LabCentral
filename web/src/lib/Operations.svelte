<script lang="ts">
  import { onMount } from "svelte";
  import { RefreshCw } from "@lucide/svelte";
  import {
    api,
    collection,
    cached,
    cachedCollection,
    type Instance,
    type Project,
  } from "./api";
  import type { components } from "./api.generated";
  import Modal from "./Modal.svelte";
  import ProviderCapacity from "./ProviderCapacity.svelte";
  import Chart from "./Chart.svelte";
  import StatusBadge from "./StatusBadge.svelte";
  type Page = components["schemas"]["OperationsPageDTO"];
  type Usage = components["schemas"]["InstanceUsage"];
  let {
    projects,
    name,
  }: { projects: Project[]; name: (i: Instance) => string } = $props();
  let project = $state("");
  let filter = $state("active");
  let labs = $state<Instance[]>(
    cachedCollection<Instance>("/lab-instances") ?? [],
  );
  let page = $state<Page | undefined>(
    cached<Page>("/admin/operations?filter=active&limit=100"),
  );
  let usage = $state<Record<string, Usage>>(
    Object.fromEntries(
      (cachedCollection<Instance>("/lab-instances") ?? []).flatMap((lab) => {
        const value = cached<Usage>(
          `/admin/lab-instances/${encodeURIComponent(lab.id)}/usage`,
        );
        return value ? [[lab.id, value]] : [];
      }),
    ),
  );
  let usageErrors = $state<Record<string, boolean>>({});
  let error = $state("");
  let busy = $state(false);
  let progressOpen = $state(false);
  let selectedOperation = $state<NonNullable<Page["items"]>[number]>();
  const progress = $derived(
    page?.items?.find((o) => o.id === selectedOperation?.id) ??
      selectedOperation,
  );
  let cursor = $state("");
  let vmPage = $state(0);
  let generation = 0,
    disposed = false,
    lastUsage = 0;
  let timer: ReturnType<typeof setTimeout>;
  let active = $derived(
    labs.filter(
      (i) => (!project || i.projectId === project) && i.phase !== "destroyed",
    ),
  );
  let vmLabs = $derived(
    active.filter((i) => i.resources?.some((r) => r.kind === "VM")),
  );
  let visible = $derived(vmLabs.slice(vmPage * 25, vmPage * 25 + 25));
  let clock = $state(Date.now());
  let measurements = $derived(
    visible.flatMap((lab) =>
      (lab.resources ?? [])
        .filter((r) => r.kind === "VM")
        .map((resource) => ({
          label: `${name(lab)} · ${lab.id.slice(-8)} / ${resource.id}`,
          usage: measured(lab.id, resource.id),
        })),
    ),
  );
  let cpu = $derived([
    {
      name: "CPU",
      data: measurements.map((m) => ({
        x: m.label,
        y: m.usage?.cpuFraction == null ? null : m.usage.cpuFraction * 100,
      })),
    },
  ]);
  let memorySeries = $derived([
    {
      name: "Memory",
      data: measurements.map((m) => ({
        x: m.label,
        y:
          m.usage?.memoryUsedBytes == null || !m.usage.memoryCapacityBytes
            ? null
            : (m.usage.memoryUsedBytes / m.usage.memoryCapacityBytes) * 100,
      })),
    },
  ]);
  let queue = $derived([
    {
      name: "Operations",
      data: [
        { x: "Queued", y: page?.queued ?? null },
        { x: "In progress", y: page?.running ?? null },
        { x: "Failed", y: page?.failed ?? null },
      ],
    },
  ]);
  let byID = $derived(Object.fromEntries(labs.map((i) => [i.id, i])));
  onMount(() => {
    void refresh();
    return () => {
      disposed = true;
      generation++;
      clearTimeout(timer);
    };
  });
  async function refresh(forceUsage = false) {
    if (busy) return;
    busy = true;
    clock = Date.now();
    clearTimeout(timer);
    const current = generation;
    try {
      const [items, result] = await Promise.all([
        collection<Instance>("/lab-instances", "cursor", { fresh: forceUsage }),
        api<Page>(
          `/admin/operations?filter=${filter}&limit=100${project ? `&projectId=${encodeURIComponent(project)}` : ""}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
          { fresh: forceUsage },
        ),
      ]);
      if (disposed || current !== generation) return;
      labs = items;
      page = result;
      error = "";
      if (vmPage * 25 >= vmLabs.length) vmPage = 0;
      if (forceUsage || Date.now() - lastUsage > 10000) {
        lastUsage = Date.now();
        const pending = [...visible];
        await Promise.all(
          Array.from({ length: Math.min(4, pending.length) }, async () => {
            while (pending.length) {
              const lab = pending.shift()!;
              try {
                const result = await api<Usage>(
                  `/admin/lab-instances/${encodeURIComponent(lab.id)}/usage`,
                  { fresh: forceUsage },
                );
                if (!disposed && current === generation) {
                  usage[lab.id] = result;
                  usageErrors[lab.id] = false;
                }
              } catch {
                if (!disposed && current === generation) {
                  delete usage[lab.id];
                  usageErrors[lab.id] = true;
                }
              }
            }
          }),
        );
      }
    } catch (cause) {
      if (!disposed && current === generation)
        error = cause instanceof Error ? cause.message : "Updates unavailable.";
    } finally {
      busy = false;
      if (!disposed)
        timer = setTimeout(
          () => void refresh(current !== generation),
          current !== generation ? 0 : 5000,
        );
    }
  }
  function reset() {
    generation++;
    cursor = "";
    vmPage = 0;
    page = undefined;
    usage = {};
    usageErrors = {};
    lastUsage = 0;
    if (!busy) void refresh(true);
  }
  function measured(id: string, resource: string) {
    const sample = usage[id]?.items?.find((i) => i.resourceId === resource);
    if (
      usageErrors[id] ||
      sample?.status !== "available" ||
      !sample.usage ||
      !Number.isFinite(new Date(sample.usage.sampledAt).getTime()) ||
      (sample?.usage &&
        clock - new Date(sample.usage.sampledAt).getTime() > 30000)
    )
      return undefined;
    return sample.usage;
  }
  const memory = (n: number | undefined) =>
    n === undefined ? "—" : `${(n / 1048576).toFixed(0)} MiB`;
</script>

<div class="mb-6 flex flex-wrap items-center gap-3">
  <label class="flex items-center gap-2 text-sm"
    >Project<select
      class="select select-sm w-56"
      aria-label="Operations project"
      bind:value={project}
      onchange={reset}
      ><option value="">All projects</option>{#each projects as p}<option
          value={p.id}>{p.name}</option
        >{/each}</select
    ></label
  >
  <button
    class="btn btn-sm ml-auto"
    disabled={busy}
    onclick={() => refresh(true)}><RefreshCw size={16} />Refresh</button
  >
</div>
{#if error}<p role="alert" class="mb-4 text-error">
    Updates paused. {error}
  </p>{/if}
<div
  class="stats stats-vertical mb-8 w-full border border-base-300 sm:stats-horizontal"
>
  <div class="stat">
    <div class="stat-title">Active labs</div>
    <div class="stat-value text-2xl">{active.length}</div>
  </div>
  <div class="stat">
    <div class="stat-title">Queued</div>
    <div class="stat-value text-2xl">{page?.queued ?? "—"}</div>
  </div>
  <div class="stat">
    <div class="stat-title">In progress</div>
    <div class="stat-value text-2xl">{page?.running ?? "—"}</div>
  </div>
  <div class="stat">
    <div class="stat-title">Failed operations</div>
    <div class="stat-value text-2xl">{page?.failed ?? "—"}</div>
  </div>
</div>
<div class="mb-8 grid gap-5 xl:grid-cols-3">
  <Chart
    title="Operation status"
    description="Current counts for the selected project scope."
    series={queue}
  />
  <Chart
    title="VM CPU"
    description="Latest available samples for this page of VMs."
    series={cpu}
    percent
  />
  <Chart
    title="VM memory"
    description="Used memory as a share of VM capacity, including guest cache."
    series={memorySeries}
    percent
  />
</div>
<ProviderCapacity />
<section class="min-w-0">
  <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
    <h2 class="text-lg font-semibold">Provisioning & cleanup</h2>
    <select
      class="select select-sm w-44"
      aria-label="Operation filter"
      bind:value={filter}
      onchange={reset}
      ><option value="active">Queue & in progress</option><option value="failed"
        >Errors</option
      ><option value="all">History</option></select
    >
  </div>
  <div class="max-w-full overflow-x-auto rounded-box border border-base-300">
    <table class="table table-sm">
      <thead
        ><tr
          ><th>Lab</th><th>Action</th><th>Status</th><th>Resources ready</th><th
            >Details</th
          ><th>Requested</th></tr
        ></thead
      >
      <tbody
        >{#each page?.items ?? [] as operation (operation.id)}{@const lab =
            byID[operation.instanceId]}<tr>
            <td
              ><a
                class="font-medium hover:underline"
                href={`/lab/?id=${operation.instanceId}`}
                >{lab ? name(lab) : "Lab"}</a
              ></td
            >
            <td>{operation.kind === "destroy" ? "Delete" : "Provision"}</td><td
              ><StatusBadge state={operation.state} /></td
            >
            <td
              >{#if lab}{lab.resources?.filter((r) => r.phase === "ready")
                  .length ?? 0} / {lab.resources?.length ?? 0}{:else}—{/if}</td
            >
            <td class="max-w-sm whitespace-normal"
              ><button
                class="btn btn-sm btn-ghost"
                onclick={() => {
                  selectedOperation = operation;
                  progressOpen = true;
                }}
                >{operation.errorCode ? "View error" : "View progress"}</button
              ></td
            >
            <td>{new Date(operation.createdAt).toLocaleString()}</td>
          </tr>{/each}</tbody
      >
    </table>
  </div>
  {#if page && !page.items?.length}<p class="py-6 text-sm text-base-content/70">
      {filter === "failed"
        ? "No provisioning errors."
        : filter === "active"
          ? "The queue is clear."
          : "No operations yet."}
    </p>{/if}
  <div class="my-3 flex gap-2">
    {#if cursor}<button
        class="btn btn-sm"
        disabled={busy}
        onclick={() => {
          cursor = "";
          void refresh();
        }}>First page</button
      >{/if}{#if page?.nextCursor}<button
        class="btn btn-sm"
        disabled={busy}
        onclick={() => {
          cursor = page!.nextCursor!;
          void refresh();
        }}>Next page</button
      >{/if}
  </div>
</section>
<section class="mt-8 min-w-0">
  <h2 class="mb-4 text-lg font-semibold">VM usage</h2>
  <div class="max-w-full overflow-x-auto rounded-box border border-base-300">
    <table class="table table-sm">
      <thead
        ><tr
          ><th>Lab / VM</th><th>Status</th><th>CPU</th><th
            >Memory used / capacity</th
          ><th>Sampled</th></tr
        ></thead
      >
      <tbody
        >{#each visible as lab (lab.id)}{#each lab.resources?.filter((r) => r.kind === "VM") ?? [] as resource}{@const u =
              measured(lab.id, resource.id)}<tr>
              <td
                ><a
                  class="font-medium hover:underline"
                  href={`/lab/?id=${lab.id}`}>{name(lab)}</a
                >
                <p class="text-xs text-base-content/60">{lab.id}</p>
                <p class="text-xs text-base-content/70">
                  {resource.id.split(".").slice(1).join(".")}
                </p></td
              ><td><StatusBadge state={resource.phase} /></td>
              <td
                >{u?.cpuFraction === undefined
                  ? "—"
                  : `${(u.cpuFraction * 100).toFixed(1)}%`}{#if u?.virtualCPUs}<span
                    class="ml-2 text-xs text-base-content/70"
                    >of {u.virtualCPUs} vCPU</span
                  >{/if}</td
              >
              <td
                >{memory(u?.memoryUsedBytes)} / {memory(
                  u?.memoryCapacityBytes,
                )}</td
              ><td class="text-xs text-base-content/70"
                >{u
                  ? new Date(u.sampledAt).toLocaleTimeString()
                  : "Unavailable"}</td
              >
            </tr>{/each}{/each}</tbody
      >
    </table>
  </div>
  {#if !vmLabs.length}<p class="py-6 text-sm text-base-content/70">
      No active VMs.
    </p>{:else}<p class="mt-3 text-xs text-base-content/70">
      Live host measurements. Memory includes guest cache.
    </p>{/if}
  {#if vmLabs.length > 25}<div class="mt-4 flex items-center gap-3">
      <button
        class="btn btn-sm"
        disabled={busy || vmPage === 0}
        onclick={() => {
          vmPage--;
          void refresh(true);
        }}>Previous</button
      ><span class="text-sm"
        >{vmPage * 25 + 1}–{Math.min((vmPage + 1) * 25, vmLabs.length)} of {vmLabs.length}
        labs</span
      ><button
        class="btn btn-sm"
        disabled={busy || (vmPage + 1) * 25 >= vmLabs.length}
        onclick={() => {
          vmPage++;
          void refresh(true);
        }}>Next</button
      >
    </div>{/if}
</section>

<Modal bind:open={progressOpen} title="Operation details"
  >{#if progress}
    <div class="flex items-center gap-3">
      <StatusBadge state={progress.state} /><span class="text-sm"
        >{progress.kind === "destroy" ? "Cleanup" : "Provisioning"} · Attempt {progress.attempt}</span
      >
    </div>
    <p class="my-4 text-sm">{progress.message || "Waiting to start"}</p>
    {#if progress.errorCode}<p class="mb-4 text-sm text-error">
        {progress.errorCode}
      </p>{/if}
    <a class="link text-sm" href={`/lab/?id=${progress.instanceId}`}>Open lab</a
    >
    <ul class="mt-5 space-y-3">
      {#each byID[progress.instanceId]?.resources ?? [] as resource}<li
          class="flex flex-wrap items-center gap-3 text-sm"
        >
          <span>{resource.id}</span><StatusBadge
            state={resource.phase}
          />{#if resource.errorCode}<span class="text-error"
              >{resource.errorCode}</span
            >{/if}
        </li>{/each}
    </ul>
  {/if}</Modal
>
