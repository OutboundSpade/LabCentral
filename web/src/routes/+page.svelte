<script lang="ts">
  import { onMount } from 'svelte';
  import { api, terminal, type Instance, type Operation, type ServerInfo } from '$lib/api';

  let server = $state<ServerInfo | null>(null);
  let source = $state('');
  let acknowledged = $state(false);
  let instances = $state<Instance[]>([]);
  let plan = $state<Array<{ resourceId: string; kind: string; action: string }> | null>(null);
  let activeOperation = $state<Operation | null>(null);
  let busy = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      [server, source] = await Promise.all([
        api<ServerInfo>('/server-info'),
        api<{ source: string }>('/examples/docker-laptop').then((value) => value.source)
      ]);
      await refreshInstances();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    }
  });

  async function refreshInstances() {
    const response = await api<{ items: Instance[] }>('/lab-instances');
    instances = response.items;
  }

  async function preview() {
    busy = true;
    error = '';
    try {
      const response = await api<{ resources: Array<{ resourceId: string; kind: string; action: string }> }>('/lab-plans', {
        method: 'POST', body: JSON.stringify({ source, acknowledgeDockerIsolationRisk: acknowledged })
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
        body: JSON.stringify({ source, acknowledgeDockerIsolationRisk: acknowledged })
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

<svelte:head><title>LabCentral · Docker laptop</title></svelte:head>

<main class="mx-auto min-h-screen max-w-7xl p-5 md:p-10">
  <header class="mb-8 flex items-center justify-between">
    <div>
      <p class="font-mono text-xs tracking-[0.28em] text-primary uppercase">Local control plane</p>
      <h1 class="mt-2 text-3xl font-semibold tracking-tight md:text-5xl">LabCentral</h1>
    </div>
    <div class="badge badge-outline badge-primary gap-2 py-3 font-mono">Docker · loopback</div>
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
        <div>
          <p class="text-xs font-semibold tracking-widest text-primary uppercase">Example A</p>
          <h2 class="card-title mt-1 text-2xl">Two-container SQLite web lab</h2>
          <p class="mt-2 text-sm text-base-content/65">A private Docker network, persistent volume, Python/SQLite web service, client, shell endpoint, and loopback URL.</p>
        </div>
        <textarea class="textarea h-72 w-full resize-y bg-base-100 font-mono text-xs leading-relaxed" bind:value={source} spellcheck="false" aria-label="Lab YAML"></textarea>
        <label class="label cursor-pointer items-start justify-start gap-3 rounded-box border border-warning/35 bg-warning/10 p-4">
          <input class="checkbox checkbox-warning mt-0.5" type="checkbox" bind:checked={acknowledged} />
          <span class="label-text leading-relaxed">I understand ordinary Docker containers are not safe for arbitrary hostile workloads and accept trusted-laptop mode for this lab.</span>
        </label>
        <div class="card-actions justify-end">
          <button class="btn btn-ghost" disabled={busy || !source} onclick={preview}>Preview plan</button>
          <button class="btn btn-primary" disabled={busy || !source || !acknowledged} onclick={launch}>{busy ? 'Working…' : 'Launch lab'}</button>
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
      <div class="rounded-box border border-dashed border-base-300 p-10 text-center text-sm text-base-content/50">Launch Example A to see its resource graph here.</div>
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
