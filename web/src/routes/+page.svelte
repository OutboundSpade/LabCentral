<script lang="ts">
  import { onMount, tick } from 'svelte';
  import RFB from '@novnc/novnc';
  import {
    api,
    ApiError,
    terminal,
    type Instance,
    type Operation,
    type ProviderTarget,
    type ServerInfo,
    type Session,
		type AccessLaunch
  } from '$lib/api';

  type ProjectChoice = { id: string; organizationId?: string; name: string };

  let server = $state<ServerInfo | null>(null);
  let session = $state<Session | null>(null);
  let authState = $state<'loading' | 'signed-out' | 'signed-in'>('loading');
  let source = $state('');
  let acknowledged = $state(false);
  let instances = $state<Instance[]>([]);
  let plan = $state<Array<{ resourceId: string; kind: string; action: string }> | null>(null);
  let activeOperation = $state<Operation | null>(null);
  let busy = $state(false);
  let loadingWorkspace = $state(false);
  let error = $state('');
  let targets = $state<ProviderTarget[]>([]);
  let selectedTargetId = $state('');
  let providerOverride = $state('');
	let accessSessionId = $state('');
	let consoleStatus = $state('Connecting to the authorized console…');
	let consoleTarget = $state<HTMLDivElement>();
	let consoleClient: RFB | null = null;
	let consoleSocket: WebSocket | null = null;

  let username = $state('');
  let password = $state('');
  let projects = $state<ProjectChoice[]>([]);
  let selectedProjectId = $state('');
  let projectToOpen = $state('');
  let showProjectChooser = $state(false);

  let organizationId = $state('default-org');
  let organizationName = $state('default-organization');
  let projectId = $state('default-project');
  let projectName = $state('default-project');
  let onboardingTargetId = $state('');

  let selectedTarget = $derived(targets.find((target) => target.id === selectedTargetId));
  let selectedProject = $derived(projects.find((project) => project.id === selectedProjectId));
  let dockerSelected = $derived(selectedTarget?.providerKind === 'docker');
  let authenticated = $derived(server?.authMode !== 'none');
  let platformAdministrator = $derived(session?.roles.includes('platform-administrator') ?? false);
  let workspaceReady = $derived(authState === 'signed-in' && (!authenticated || selectedProjectId !== '') && !showProjectChooser);

  onMount(() => {
		const match = window.location.pathname.match(/^\/access\/([A-Za-z][A-Za-z0-9_.-]{0,127})$/);
		if (match) {
			accessSessionId = match[1];
			void tick().then(connectConsole);
		} else {
			void bootstrap();
		}
		return () => {
			consoleClient?.disconnect();
			consoleSocket?.close();
		};
	});

	async function connectConsole() {
		const tokenKey = `labcentral.access-token.v1.${accessSessionId}`;
		const token = sessionStorage.getItem(tokenKey);
		sessionStorage.removeItem(tokenKey);
		if (!token || !consoleTarget) {
			consoleStatus = 'This launch token is absent or has already been consumed. Return to the lab and request a new console.';
			return;
		}
		const target = consoleTarget;
		const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const socket = new WebSocket(`${scheme}//${window.location.host}/access-proxy/v1/access/${encodeURIComponent(accessSessionId)}/websocket`, ['binary']);
		consoleSocket = socket;
		socket.binaryType = 'arraybuffer';
		socket.addEventListener('open', () => {
			socket.send(JSON.stringify({ accessSessionId, oneTimeToken: token }));
		}, { once: true });
		socket.addEventListener('message', (event) => {
			if (event.data !== '{"authorized":true}') {
				consoleStatus = 'The console authorization response was invalid.';
				socket.close();
				return;
			}
			try {
				consoleClient = new RFB(target, socket, { shared: false });
				consoleClient.scaleViewport = true;
				consoleClient.resizeSession = true;
				consoleClient.addEventListener('connect', () => {
					consoleStatus = 'Connected';
					consoleClient?.focus();
				});
				consoleClient.addEventListener('disconnect', () => {
					consoleStatus = 'Console disconnected. Return to the lab to request a new session.';
				});
				consoleClient.addEventListener('securityfailure', () => {
					consoleStatus = 'Console security negotiation failed.';
				});
			} catch {
				consoleStatus = 'The console client could not start.';
				socket.close();
			}
		}, { once: true });
		socket.addEventListener('error', () => {
			if (!consoleClient) consoleStatus = 'The console proxy is unavailable.';
		}, { once: true });
	}

  async function bootstrap() {
    try {
      server = await api<ServerInfo>('/server-info');
      onboardingTargetId = server.defaultProviderTargetId;
      if (server.authMode === 'none') {
        session = await api<Session>('/session');
        authState = 'signed-in';
        selectedProjectId = 'project.local';
        await loadWorkspace(selectedProjectId);
        return;
      }
      try {
        session = await api<Session>('/session');
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 401) {
          authState = 'signed-out';
          return;
        }
        throw cause;
      }
      authState = 'signed-in';
      restoreProjects();
      if (selectedProjectId) {
        try {
          await loadWorkspace(selectedProjectId);
        } catch (cause) {
          handleFailure(cause);
          if (authState === 'signed-in') {
            selectedProjectId = '';
            localStorage.removeItem(selectedProjectStorageKey());
            showProjectChooser = true;
          }
        }
      } else {
        showProjectChooser = true;
      }
    } catch (cause) {
      handleFailure(cause);
    }
  }

  function projectStorageKey() {
    return session ? `labcentral.projects.v1.${session.subjectId}` : '';
  }

  function selectedProjectStorageKey() {
    return session ? `labcentral.selected-project.v1.${session.subjectId}` : '';
  }

  function restoreProjects() {
    try {
      projects = JSON.parse(localStorage.getItem(projectStorageKey()) ?? '[]') as ProjectChoice[];
      selectedProjectId = localStorage.getItem(selectedProjectStorageKey()) ?? '';
      if (selectedProjectId && !projects.some((project) => project.id === selectedProjectId)) {
        projects = [...projects, { id: selectedProjectId, name: selectedProjectId }];
      }
    } catch {
      projects = [];
      selectedProjectId = '';
    }
  }

  function rememberProject(project: ProjectChoice) {
    projects = [...projects.filter((existing) => existing.id !== project.id), project];
    selectedProjectId = project.id;
    localStorage.setItem(projectStorageKey(), JSON.stringify(projects));
    localStorage.setItem(selectedProjectStorageKey(), project.id);
  }

  async function login() {
    busy = true;
    error = '';
    try {
      session = await api<Session>('/auth/local/login', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      });
      password = '';
      authState = 'signed-in';
      restoreProjects();
      if (selectedProjectId) {
        try {
          await loadWorkspace(selectedProjectId);
        } catch (cause) {
          handleFailure(cause);
          if (authState === 'signed-in') {
            selectedProjectId = '';
            localStorage.removeItem(selectedProjectStorageKey());
            showProjectChooser = true;
          }
        }
      } else {
        showProjectChooser = true;
      }
    } catch (cause) {
      handleFailure(cause);
    } finally {
      busy = false;
    }
  }

  async function logout() {
    busy = true;
    error = '';
    try {
      await api<void>('/auth/logout', { method: 'POST' });
    } catch (cause) {
      if (!(cause instanceof ApiError && cause.status === 401)) {
        handleFailure(cause);
        busy = false;
        return;
      }
    }
    session = null;
    authState = 'signed-out';
    selectedProjectId = '';
    projects = [];
    targets = [];
    instances = [];
    source = '';
    activeOperation = null;
    plan = null;
    showProjectChooser = false;
    busy = false;
  }

  async function openProject(id: string, name = id, organization = '') {
    const normalized = id.trim();
    if (!normalized) return;
    loadingWorkspace = true;
    error = '';
    try {
      await loadWorkspace(normalized);
      rememberProject({ id: normalized, name: name.trim() || normalized, organizationId: organization || undefined });
      projectToOpen = '';
      showProjectChooser = false;
    } catch (cause) {
      handleFailure(cause);
    } finally {
      loadingWorkspace = false;
    }
  }

  async function onboardFirstProject() {
    if (!session || !server) return;
    busy = true;
    error = '';
    try {
      const organization = organizationId.trim();
      const project = projectId.trim();
      await api('/organizations', {
        method: 'POST',
        body: JSON.stringify({ id: organization, name: organizationName.trim() })
      });
      await api('/projects', {
        method: 'POST',
        body: JSON.stringify({ id: project, organizationId: organization, name: projectName.trim() })
      });
      await api('/memberships', {
        method: 'POST',
        body: JSON.stringify({
          id: `membership.${crypto.randomUUID()}`,
          userId: session.subjectId,
          organizationId: organization
        })
      });
      const availableTargets = await api<{ items: ProviderTarget[] }>(`/provider-targets?projectId=${encodeURIComponent(project)}`);
      const targetId = onboardingTargetId.trim() || server.defaultProviderTargetId || availableTargets.items[0]?.id;
      if (!targetId || !availableTargets.items.some((target) => target.id === targetId)) {
        throw new Error('Choose a registered provider target before completing the project.');
      }
      await api('/provider-target-grants', {
        method: 'POST',
        body: JSON.stringify({
          id: `target-grant.${crypto.randomUUID()}`,
          targetId,
          scopeKind: 'project',
          scopeId: project
        })
      });
      onboardingTargetId = targetId;
      await loadWorkspace(project);
      rememberProject({ id: project, organizationId: organization, name: projectName.trim() });
      showProjectChooser = false;
    } catch (cause) {
      handleFailure(cause);
    } finally {
      busy = false;
    }
  }

  async function loadWorkspace(project: string) {
    if (!server) return;
    const query = server.authMode === 'none' ? '' : `?projectId=${encodeURIComponent(project)}`;
    const targetResponse = await api<{ items: ProviderTarget[] }>(`/provider-targets${query}`);
    targets = targetResponse.items;
    let fallbackSource = '';
    if (server.authMode === 'none') {
      fallbackSource = await api<{ source: string }>('/examples/docker-laptop').then((value) => value.source);
    }
    chooseTarget(
      targets.some((target) => target.id === server?.defaultProviderTargetId)
        ? server.defaultProviderTargetId
        : targets[0]?.id ?? '',
      fallbackSource
    );
    await refreshInstances(project);
  }

  async function changeProject(event: Event) {
    const id = (event.currentTarget as HTMLSelectElement).value;
    if (!id) return;
    await openProject(id, projects.find((project) => project.id === id)?.name ?? id);
  }

  async function refreshInstances(project = selectedProjectId) {
    const response = await api<{ items: Instance[] }>('/lab-instances');
    instances = authenticated ? response.items.filter((instance) => instance.projectId === project) : response.items;
  }

  function chooseTarget(targetId: string, fallbackSource = '') {
    selectedTargetId = targetId;
    const example = targets.find((target) => target.id === targetId)?.example;
    source = example?.source || fallbackSource || '';
    providerOverride = example?.providerOverride ? JSON.stringify(example.providerOverride, null, 2) : '';
    acknowledged = false;
    plan = null;
  }

  function labRequest() {
    if (!session) throw new Error('Sign in before creating a lab.');
    return {
      source,
      acknowledgeDockerIsolationRisk: acknowledged,
      projectId: selectedProjectId || 'project.local',
      ownerKind: 'user',
      ownerId: session.subjectId,
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
      handleFailure(cause);
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
      handleFailure(cause);
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
      handleFailure(cause);
    } finally {
      busy = false;
    }
  }

	async function openConsole(instance: Instance, resourceId: string) {
		busy = true;
		error = '';
		try {
			const launch = await api<AccessLaunch>('/access-sessions', {
				method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() },
				body: JSON.stringify({ instanceId: instance.id, resourceId, method: 'vnc', durationSeconds: 1800 })
			});
			if (!launch.oneTimeToken || !launch.launchUrl) throw new Error('The access service returned an incomplete launch.');
			sessionStorage.setItem(`labcentral.access-token.v1.${launch.session.id}`, launch.oneTimeToken);
			window.location.assign(launch.launchUrl);
		} catch (cause) {
			handleFailure(cause);
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

  function handleFailure(cause: unknown) {
    if (cause instanceof ApiError && cause.status === 401 && server?.authMode !== 'none') {
      session = null;
      authState = 'signed-out';
      showProjectChooser = false;
    }
    error = cause instanceof Error ? cause.message : String(cause);
  }
</script>

<svelte:head><title>{accessSessionId ? 'Lab console · LabCentral' : 'LabCentral'}</title></svelte:head>

{#if accessSessionId}
	<main class="flex min-h-screen flex-col bg-black p-3 text-white">
		<header class="mb-3 flex items-center justify-between gap-4"><div><p class="text-xs tracking-widest text-primary uppercase">Authorized VNC session</p><p class="font-mono text-xs opacity-60">{accessSessionId}</p></div><a class="btn btn-sm btn-outline" href="/">Return to labs</a></header>
		<p class="mb-2 text-sm" aria-live="polite">{consoleStatus}</p>
		<div bind:this={consoleTarget} class="min-h-0 flex-1 overflow-hidden rounded-lg border border-white/20 bg-black"></div>
	</main>
{:else}
<main class="mx-auto min-h-screen max-w-7xl p-5 md:p-10">
  <header class="mb-8 flex flex-wrap items-center justify-between gap-4">
    <div>
      <p class="font-mono text-xs tracking-[0.28em] text-primary uppercase">Lab control plane</p>
      <h1 class="mt-2 text-3xl font-semibold tracking-tight md:text-5xl">LabCentral</h1>
    </div>
    {#if session}
      <div class="flex items-center gap-3">
        <div class="text-right"><p class="text-sm font-medium">{session.displayName}</p><p class="font-mono text-[11px] text-base-content/50">{session.subjectId}</p></div>
        {#if authenticated}<button class="btn btn-sm btn-ghost" disabled={busy} onclick={logout}>Sign out</button>{/if}
      </div>
    {/if}
  </header>

  {#if server?.authMode === 'none'}
    <div role="alert" class="alert alert-warning mb-7 items-start shadow-lg">
      <span class="mt-0.5 text-xl">⚠</span>
      <div><h2 class="font-semibold">No-auth trusted-laptop mode</h2><p class="text-sm leading-relaxed">{server.noAuthWarning}</p></div>
    </div>
  {/if}

  {#if error}
    <div role="alert" class="alert alert-error mb-7"><span>{error}</span><button class="btn btn-sm btn-ghost" aria-label="Dismiss error" onclick={() => error = ''}>Dismiss</button></div>
  {/if}

  {#if authState === 'loading'}
    <div class="flex min-h-80 items-center justify-center"><span class="loading loading-spinner loading-lg text-primary"></span></div>
  {:else if authState === 'signed-out' && server}
    <section class="mx-auto max-w-md card border border-base-300 bg-base-200/85 shadow-2xl">
      <div class="card-body gap-5">
        <div><p class="text-xs font-semibold tracking-widest text-primary uppercase">Secure workspace</p><h2 class="card-title mt-2 text-2xl">Sign in to LabCentral</h2><p class="mt-2 text-sm text-base-content/60">Your labs and provider access are limited to your assigned projects.</p></div>
        {#if server.authMode === 'local'}
          <form class="space-y-4" onsubmit={(event) => { event.preventDefault(); login(); }}>
            <label class="form-control w-full"><span class="label-text mb-2">Username</span><input class="input input-bordered w-full bg-base-100" autocomplete="username" bind:value={username} required maxlength="63" /></label>
            <label class="form-control w-full"><span class="label-text mb-2">Password</span><input class="input input-bordered w-full bg-base-100" type="password" autocomplete="current-password" bind:value={password} required minlength="14" maxlength="1024" /></label>
            <button class="btn btn-primary w-full" type="submit" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
          </form>
        {:else if server.authMode === 'oidc'}
          <a class="btn btn-primary w-full" href="/api/v1/auth/oidc/begin?redirect=%2F">Continue with your identity provider</a>
        {/if}
      </div>
    </section>
  {:else if authState === 'signed-in' && authenticated && (showProjectChooser || !selectedProjectId)}
    <div class="grid gap-7 lg:grid-cols-2">
      <section class="card border border-base-300 bg-base-200/80 shadow-xl">
        <div class="card-body gap-5">
          <div><p class="text-xs tracking-widest text-primary uppercase">Project access</p><h2 class="card-title mt-2 text-2xl">Open a project</h2></div>
          {#if projects.length}
            <div class="space-y-2">
              {#each projects as project}
                <button class="btn btn-outline w-full justify-between" disabled={loadingWorkspace} onclick={() => openProject(project.id, project.name, project.organizationId)}><span>{project.name}</span><span class="font-mono text-xs opacity-60">{project.id}</span></button>
              {/each}
            </div>
            <div class="divider text-xs">or</div>
          {/if}
          <form class="flex gap-2" onsubmit={(event) => { event.preventDefault(); openProject(projectToOpen); }}>
            <input class="input input-bordered min-w-0 flex-1 bg-base-100 font-mono" placeholder="Project ID" bind:value={projectToOpen} required maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" />
            <button class="btn btn-primary" type="submit" disabled={loadingWorkspace}>{loadingWorkspace ? 'Opening…' : 'Open'}</button>
          </form>
          <p class="text-xs leading-relaxed text-base-content/50">Enter a project ID assigned by your administrator. This browser remembers successful selections for your account.</p>
        </div>
      </section>

      {#if platformAdministrator}
        <section class="card border border-primary/30 bg-base-200/80 shadow-xl">
          <div class="card-body gap-4">
            <div><p class="text-xs tracking-widest text-primary uppercase">First-time setup</p><h2 class="card-title mt-2 text-2xl">Create the first project</h2><p class="mt-2 text-sm text-base-content/60">Creates an organization and project, adds your administrator membership, and grants one provider target.</p></div>
            <form class="grid gap-4 sm:grid-cols-2" onsubmit={(event) => { event.preventDefault(); onboardFirstProject(); }}>
              <label class="form-control"><span class="label-text mb-2">Organization ID</span><input class="input input-bordered bg-base-100 font-mono" bind:value={organizationId} required maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" /></label>
              <label class="form-control"><span class="label-text mb-2">Organization name</span><input class="input input-bordered bg-base-100" bind:value={organizationName} required maxlength="63" pattern="[a-z][a-z0-9-]*" title="Use lowercase letters, numbers, and hyphens." /></label>
              <label class="form-control"><span class="label-text mb-2">Project ID</span><input class="input input-bordered bg-base-100 font-mono" bind:value={projectId} required maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" /></label>
              <label class="form-control"><span class="label-text mb-2">Project name</span><input class="input input-bordered bg-base-100" bind:value={projectName} required maxlength="63" pattern="[a-z][a-z0-9-]*" title="Use lowercase letters, numbers, and hyphens." /></label>
              <label class="form-control sm:col-span-2"><span class="label-text mb-2">Provider target ID</span><input class="input input-bordered bg-base-100 font-mono" bind:value={onboardingTargetId} placeholder="Uses the default target when blank" maxlength="128" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" /></label>
              <button class="btn btn-primary sm:col-span-2" type="submit" disabled={busy}>{busy ? 'Creating project…' : 'Create and open project'}</button>
            </form>
          </div>
        </section>
      {/if}
    </div>
  {:else if workspaceReady}
    <div class="mb-7 flex flex-wrap items-end justify-between gap-4 rounded-box border border-base-300 bg-base-200/65 px-5 py-4">
      <div><p class="text-xs tracking-widest text-primary uppercase">Active project</p><p class="mt-1 font-medium">{selectedProject?.name ?? selectedProjectId}</p><p class="font-mono text-[11px] text-base-content/45">{selectedProjectId}</p></div>
      <div class="flex items-center gap-2">
        {#if authenticated && projects.length > 1}
          <select class="select select-sm select-bordered bg-base-100" value={selectedProjectId} onchange={changeProject} disabled={loadingWorkspace || busy} aria-label="Active project">
            {#each projects as project}<option value={project.id}>{project.name}</option>{/each}
          </select>
        {/if}
        {#if authenticated}<button class="btn btn-sm btn-ghost" onclick={() => showProjectChooser = true}>Change project</button>{/if}
        <div class="badge badge-outline badge-primary gap-2 py-3 font-mono">{selectedTarget?.providerKind ?? 'provider'} · {selectedTarget?.status ?? 'unavailable'}</div>
      </div>
    </div>

    <div class="grid gap-7 lg:grid-cols-[1.25fr_0.75fr]">
      <section class="card border border-base-300 bg-base-200/80 shadow-2xl backdrop-blur">
        <div class="card-body gap-5">
          <label class="form-control w-full">
            <span class="label-text mb-2 text-xs font-semibold tracking-widest text-primary uppercase">Provider target</span>
            <select class="select select-bordered w-full bg-base-100" value={selectedTargetId} onchange={(event) => chooseTarget(event.currentTarget.value)} disabled={busy}>
              {#each targets as target}<option value={target.id}>{target.id} · {target.providerKind} · {target.status}</option>{/each}
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
            <p class="mt-2 text-sm text-base-content/65">{dockerSelected ? 'A private Docker network, persistent volume, Python/SQLite web service, client, shell endpoint, and loopback URL.' : 'The selected target accepts only its administrator-approved VM, storage, and network profiles.'}</p>
          </div>
          <textarea class="textarea h-72 w-full resize-y bg-base-100 font-mono text-xs leading-relaxed" bind:value={source} spellcheck="false" aria-label="Lab YAML"></textarea>
          {#if !dockerSelected}
            <label class="form-control w-full"><span class="label-text mb-2 text-xs font-semibold tracking-widest text-primary uppercase">Approved provider profiles</span><textarea class="textarea h-28 w-full resize-y bg-base-100 font-mono text-xs leading-relaxed" bind:value={providerOverride} spellcheck="false" aria-label="Provider override JSON"></textarea></label>
          {/if}
          {#if dockerSelected}
            <label class="label cursor-pointer items-start justify-start gap-3 rounded-box border border-warning/35 bg-warning/10 p-4"><input class="checkbox checkbox-warning mt-0.5" type="checkbox" bind:checked={acknowledged} /><span class="label-text leading-relaxed">I understand ordinary Docker containers are not safe for arbitrary hostile workloads and accept trusted-laptop mode for this lab.</span></label>
          {/if}
          <div class="card-actions justify-end">
            <button class="btn btn-ghost" disabled={busy || !source || !selectedTargetId} onclick={preview}>Preview plan</button>
            <button class="btn btn-primary" disabled={busy || !source || !selectedTargetId || (dockerSelected && !acknowledged)} onclick={launch}>{busy ? 'Working…' : 'Launch lab'}</button>
          </div>
        </div>
      </section>

      <aside class="space-y-7">
        <section class="card border border-base-300 bg-base-200/80 shadow-xl">
          <div class="card-body"><h2 class="card-title text-lg">Operation</h2>{#if activeOperation}<div class="mt-2 flex items-center gap-3">{#if !terminal(activeOperation.state)}<span class="loading loading-spinner loading-sm text-primary"></span>{/if}<span class="badge badge-outline font-mono">{activeOperation.state}</span></div><p class="mt-3 text-sm">{activeOperation.message}</p><p class="mt-2 break-all font-mono text-[11px] text-base-content/45">{activeOperation.id}</p>{:else}<p class="text-sm text-base-content/55">No operation is running.</p>{/if}</div>
        </section>
        {#if plan}
          <section class="card border border-base-300 bg-base-200/80 shadow-xl"><div class="card-body"><h2 class="card-title text-lg">Pinned plan</h2><div class="mt-2 space-y-2">{#each plan as step}<div class="flex items-center justify-between rounded-lg bg-base-100 px-3 py-2 text-sm"><span class="font-mono text-xs">{step.resourceId}</span><span class="badge badge-sm">{step.action}</span></div>{/each}</div></div></section>
        {/if}
      </aside>
    </div>

    <section class="mt-10">
      <div class="mb-4 flex items-end justify-between"><div><p class="text-xs tracking-widest text-primary uppercase">Observed state</p><h2 class="mt-1 text-2xl font-semibold">Lab instances</h2></div><button class="btn btn-sm btn-ghost" onclick={() => refreshInstances()}>Refresh</button></div>
      {#if instances.length === 0}
        <div class="rounded-box border border-dashed border-base-300 p-10 text-center text-sm text-base-content/50">Launch a lab to see its resource graph here.</div>
      {:else}
        <div class="grid gap-4 md:grid-cols-2">
          {#each instances as instance}
            <article class="card border border-base-300 bg-base-200/75"><div class="card-body gap-4"><div class="flex items-center justify-between gap-3"><span class="font-mono text-xs text-base-content/55">{instance.id}</span><span class="badge badge-outline">{instance.phase}</span></div>{#if instance.outputs['web-url']}<a class="link link-primary break-all text-sm" href={String(instance.outputs['web-url'])} target="_blank" rel="noreferrer">{String(instance.outputs['web-url'])}</a>{/if}<div class="grid grid-cols-2 gap-2">{#each instance.resources ?? [] as resource}<div class="rounded-lg bg-base-100 px-3 py-2"><p class="truncate font-mono text-[11px]">{resource.id}</p><div class="mt-1 flex items-center justify-between gap-2"><p class="text-xs text-base-content/50">{resource.phase}</p>{#if server?.uiFeatures.includes('browser-vnc') && resource.kind === 'VM' && resource.phase === 'ready'}<button class="btn btn-xs btn-primary btn-outline" disabled={busy} onclick={() => openConsole(instance, resource.id)}>Console</button>{/if}</div></div>{/each}</div><div class="card-actions justify-end"><button class="btn btn-sm btn-error btn-outline" disabled={busy || instance.phase === 'destroyed' || instance.phase === 'destroying'} onclick={() => destroy(instance)}>Destroy</button></div></div></article>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</main>
{/if}
