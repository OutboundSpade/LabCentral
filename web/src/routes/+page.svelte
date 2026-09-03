<script lang="ts">
  import { onMount } from 'svelte';
  import { api, terminal, type Instance, type Operation, type ProviderTarget, type ServerInfo } from '$lib/api';

  let server = $state<ServerInfo | null>(null);
  let source = $state('');
  let acknowledged = $state(false);
  let instances = $state<Instance[]>([]);
  let plan = $state<Array<{ resourceId: string; kind: string; action: string }> | null>(null);
  let activeOperation = $state<Operation | null>(null);
  let busy = $state(false);
  let error = $state('');
  let targets = $state<ProviderTarget[]>([]);
  let selectedTargetId = $state('');
  let providerOverride = $state('');

  let selectedTarget = $derived(targets.find((target) => target.id === selectedTargetId));
  let dockerSelected = $derived(selectedTarget?.providerKind === 'docker');

  onMount(async () => {
    try {
      [server, source, targets] = await Promise.all([
        api<ServerInfo>('/server-info'),
        api<{ source: string }>('/examples/docker-laptop').then((value) => value.source),
        api<{ items: ProviderTarget[] }>('/provider-targets').then((value) => value.items)
      ]);
      chooseTarget(server.defaultProviderTargetId || targets[0]?.id || '', source);
      await refreshInstances();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    }
  });

  async function refreshInstances() {
    const response = await api<{ items: Instance[] }>('/lab-instances');
    instances = response.items;
  }

  function chooseTarget(targetId: string, fallbackSource = '') {
    selectedTargetId = targetId;
    const example = targets.find((target) => target.id === targetId)?.example;
    source = example?.source || fallbackSource || source;
    providerOverride = example?.providerOverride ? JSON.stringify(example.providerOverride, null, 2) : '';
    acknowledged = false;
    plan = null;
  }

  function labRequest() {
    return {
      source,
      acknowledgeDockerIsolationRisk: acknowledged,
      providerTargetId: selectedTargetId,
      ...(providerOverride.trim() ? { providerOverride: JSON.parse(providerOverride) } : {})
    };
  }

  async function preview() {
    busy = true;
    error = '';
    try {
      const response = await api<{ resources: Array<{ resourceId: string; kind: string; action: string }> }>('/lab-plans', {
        method: 'POST', body: JSON.stringify(labRequest())
      });
      plan = response.resources;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      busy = false;
    }
  }

  async function launch() {
    busy = true;
    error = '';
    try {
      const response = await api<{ operation: Operation }>('/lab-instances', {
        method: 'POST',
        headers: { 'Idempotency-Key': crypto.randomUUID() },
        body: JSON.stringify(labRequest())
      });
      activeOperation = response.operation;
      await poll(response.operation.id);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      busy = false;
    }
  }

  async function destroy(instance: Instance) {
    busy = true;
    error = '';
    try {
      const response = await api<{ operation: Operation }>(`/lab-instances/${instance.id}/actions/destroy`, {
        method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: '{}'
      });
      activeOperation = response.operation;
      await poll(response.operation.id);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      busy = false;
    }
  }

  async function poll(id: string) {
    while (true) {
      activeOperation = await api<Operation>(`/operations/${id}`);
      await refreshInstances();
      if (terminal(activeOperation.state)) return;
      await new Promise((resolve) => setTimeout(resolve, 450));
    }
  }
</script>

<svelte:head><title>LabCentral</title></svelte:head>

<main class="mx-auto min-h-screen max-w-7xl p-5 md:p-10">
  <header class="mb-8 flex items-center justify-between">
    <div>
      <p class="font-mono text-xs tracking-[0.28em] text-primary uppercase">Local control plane</p>
      <h1 class="mt-2 text-3xl font-semibold tracking-tight md:text-5xl">LabCentral</h1>
    </div>
    <div class="badge badge-outline badge-primary gap-2 py-3 font-mono">{selectedTarget?.providerKind ?? 'provider'} · loopback</div>
  </header>

  {#if server}
    <div role="alert" class="alert alert-warning mb-7 items-start shadow-lg">
      <span class="mt-0.5 text-xl">⚠</span>
      <div>
        <h2 class="font-semibold">No-auth trusted-laptop mode</h2>
        <p class="text-sm leading-relaxed">{server.noAuthWarning}</p>
      </div>
    </div>
  {/if}

  {#if error}
    <div role="alert" class="alert alert-error mb-7"><span>{error}</span></div>
  {/if}

  <div class="grid gap-7 lg:grid-cols-[1.25fr_0.75fr]">
    <section class="card border border-base-300 bg-base-200/80 shadow-2xl backdrop-blur">
      <div class="card-body gap-5">
        <label class="form-control w-full">
          <span class="label-text mb-2 text-xs font-semibold tracking-widest text-primary uppercase">Provider target</span>
          <select class="select select-bordered w-full bg-base-100" value={selectedTargetId} onchange={(event) => chooseTarget(event.currentTarget.value)} disabled={busy}>
            {#each targets as target}
              <option value={target.id}>{target.id} · {target.providerKind} · {target.status}</option>
            {/each}
          </select>
        </label>
        {#if selectedTarget}
          <div class="flex flex-wrap items-center gap-2 text-xs text-base-content/60">
            <span class:badge-success={selectedTarget.status === 'fresh'} class:badge-warning={selectedTarget.status !== 'fresh'} class="badge badge-sm badge-outline">{selectedTarget.status}</span>
            <span>Protocol {selectedTarget.protocolMajor}.{selectedTarget.protocolMinor}</span>
            {#if selectedTarget.validUntil}<span>Evidence valid until {new Date(selectedTarget.validUntil).toLocaleString()}</span>{/if}
          </div>
        {/if}
        <div>
          <p class="text-xs font-semibold tracking-widest text-primary uppercase">Lab declaration</p>
          <h2 class="card-title mt-1 text-2xl">{dockerSelected ? 'Two-container SQLite web lab' : 'Qualified VM lab'}</h2>
          <p class="mt-2 text-sm text-base-content/65">{dockerSelected ? 'A private Docker network, persistent volume, Python/SQLite web service, client, shell endpoint, and loopback URL.' : 'The selected Proxmox target accepts only its signed, administrator-approved VM, storage, and network profiles.'}</p>
        </div>
        <textarea class="textarea h-72 w-full resize-y bg-base-100 font-mono text-xs leading-relaxed" bind:value={source} spellcheck="false" aria-label="Lab YAML"></textarea>
        {#if !dockerSelected}
          <label class="form-control w-full">
            <span class="label-text mb-2 text-xs font-semibold tracking-widest text-primary uppercase">Approved provider profiles</span>
            <textarea class="textarea h-28 w-full resize-y bg-base-100 font-mono text-xs leading-relaxed" bind:value={providerOverride} spellcheck="false" aria-label="Provider override JSON"></textarea>
          </label>
        {/if}
        {#if dockerSelected}
          <label class="label cursor-pointer items-start justify-start gap-3 rounded-box border border-warning/35 bg-warning/10 p-4">
            <input class="checkbox checkbox-warning mt-0.5" type="checkbox" bind:checked={acknowledged} />
            <span class="label-text leading-relaxed">I understand ordinary Docker containers are not safe for arbitrary hostile workloads and accept trusted-laptop mode for this lab.</span>
          </label>
        {/if}
        <div class="card-actions justify-end">
          <button class="btn btn-ghost" disabled={busy || !source} onclick={preview}>Preview plan</button>
          <button class="btn btn-primary" disabled={busy || !source || !selectedTargetId || (dockerSelected && !acknowledged)} onclick={launch}>{busy ? 'Working…' : 'Launch lab'}</button>
        </div>
      </div>
    </section>

    <aside class="space-y-7">
      <section class="card border border-base-300 bg-base-200/80 shadow-xl">
        <div class="card-body">
          <h2 class="card-title text-lg">Operation</h2>
          {#if activeOperation}
            <div class="mt-2 flex items-center gap-3">
              {#if !terminal(activeOperation.state)}<span class="loading loading-spinner loading-sm text-primary"></span>{/if}
              <span class="badge badge-outline font-mono">{activeOperation.state}</span>
            </div>
            <p class="mt-3 text-sm">{activeOperation.message}</p>
            <p class="mt-2 break-all font-mono text-[11px] text-base-content/45">{activeOperation.id}</p>
          {:else}
            <p class="text-sm text-base-content/55">No operation is running.</p>
          {/if}
        </div>
      </section>

      {#if plan}
        <section class="card border border-base-300 bg-base-200/80 shadow-xl">
          <div class="card-body">
            <h2 class="card-title text-lg">Pinned plan</h2>
            <div class="mt-2 space-y-2">
              {#each plan as step}
                <div class="flex items-center justify-between rounded-lg bg-base-100 px-3 py-2 text-sm">
                  <span class="font-mono text-xs">{step.resourceId}</span><span class="badge badge-sm">{step.action}</span>
                </div>
              {/each}
            </div>
          </div>
        </section>
      {/if}
    </aside>
  </div>

  <section class="mt-10">
    <div class="mb-4 flex items-end justify-between">
      <div><p class="text-xs tracking-widest text-primary uppercase">Observed state</p><h2 class="mt-1 text-2xl font-semibold">Lab instances</h2></div>
      <button class="btn btn-sm btn-ghost" onclick={refreshInstances}>Refresh</button>
    </div>
    {#if instances.length === 0}
      <div class="rounded-box border border-dashed border-base-300 p-10 text-center text-sm text-base-content/50">Launch a lab to see its resource graph here.</div>
    {:else}
      <div class="grid gap-4 md:grid-cols-2">
        {#each instances as instance}
          <article class="card border border-base-300 bg-base-200/75">
            <div class="card-body gap-4">
              <div class="flex items-center justify-between gap-3">
                <span class="font-mono text-xs text-base-content/55">{instance.id}</span>
                <span class="badge badge-outline">{instance.phase}</span>
              </div>
              {#if instance.outputs['web-url']}
                <a class="link link-primary break-all text-sm" href={String(instance.outputs['web-url'])} target="_blank" rel="noreferrer">{String(instance.outputs['web-url'])}</a>
              {/if}
              <div class="grid grid-cols-2 gap-2">
                {#each instance.resources ?? [] as resource}
                  <div class="rounded-lg bg-base-100 px-3 py-2"><p class="truncate font-mono text-[11px]">{resource.id}</p><p class="mt-1 text-xs text-base-content/50">{resource.phase}</p></div>
                {/each}
              </div>
              <div class="card-actions justify-end">
                <button class="btn btn-sm btn-error btn-outline" disabled={busy || instance.phase === 'destroyed' || instance.phase === 'destroying'} onclick={() => destroy(instance)}>Destroy</button>
              </div>
            </div>
          </article>
        {/each}
      </div>
    {/if}
  </section>
</main>
