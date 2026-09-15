<script lang="ts">
  import { onMount } from "svelte";
  import { afterNavigate } from "$app/navigation";
  import { page } from "$app/state";
  import {
    FlaskConical,
    Plus,
    BookOpen,
    LogOut,
    ChevronRight,
    Search,
    RefreshCw,
    CircleAlert,
    X,
    Menu,
    ShieldCheck,
    Server,
    Play,
    FingerprintPattern,
  } from "@lucide/svelte";
  import LabDetail from "./LabDetail.svelte";
  import LabList from "./LabList.svelte";
  import Operations from "./Operations.svelte";
  import Navigation from "./Navigation.svelte";
  import Workspaces from "./Workspaces.svelte";
  import Plugins from "./Plugins.svelte";
  import { routeViews, titles, isGlobalView } from "./navigation";
  import StatusBadge from "./StatusBadge.svelte";
  import ParameterInputs from "./ParameterInputs.svelte";
  import DeclaredInputs from "./DeclaredInputs.svelte";
  import People from "./People.svelte";
  import Policies from "./Policies.svelte";
  import {
    api,
    collection,
    ApiError,
    mutationHeaders,
    terminal,
    type Project,
    type Person,
    type LabTemplate,
    type Revision,
    type TemplateContent,
    type Instance,
    type Operation,
    type ServerInfo,
    type Session,
    type ProviderTarget,
    type InputValue,
    type AccessLaunch,
    type Batch,
  } from "./api";

  let view = $derived(
    routeViews[
      page.url.pathname.replace(/^\/|\/$/g, "") as keyof typeof routeViews
    ] ?? "labs",
  );
  let server = $state<ServerInfo>();
  let session = $state<Session>();
  let loading = $state(true);
  let busy = $state(false);
  let error = $state("");
  let notice = $state("");
  let stale = $state(false);
  let username = $state("");
  let password = $state("");
  let projects = $state<Project[]>([]);
  let projectId = $state("");
  let targets = $state<ProviderTarget[]>([]);
  let targetId = $state("");
  let instances = $state<Instance[]>([]);
  let templates = $state<LabTemplate[]>([]);
  let revisionTemplates = $state<Record<string, LabTemplate>>({});
  let revisions = $state<Revision[]>([]);
  let templateId = $state("");
  let revisionId = $state("");
  let content = $state<TemplateContent>();
  let inputs = $state<InputValue[]>([]);
  let inputsValid = $state(true);
  let acknowledge = $state(false);
  let source = $state("");
  let templateName = $state("");
  let templateDescription = $state("");
  let providerOverride = $state("");
  let roster = $state<Person[]>([]);
  let selectedOwners = $state<string[]>([]);
  let batch = $state<Batch>();
  let activeOperation = $state<Operation>();
  let search = $state("");
  let showEnded = $state(false);
  let detailId = $derived(page.url.searchParams.get("id") ?? "");
  let drawerOpen = $state(false);
  let endDialog = $state<HTMLDialogElement>();
  let ending = $state<Instance>();
  let disposed = false;
  let generation = 0;
  let selection = 0;
  let refreshTimer: ReturnType<typeof setTimeout>;
  let globalView = $derived(isGlobalView(view));
  let project = $derived(projects.find((p) => p.id === projectId));
  let template = $derived(templates.find((t) => t.id === templateId));
  let administrator = $derived(
    session?.roles?.includes("platform-administrator") ?? false,
  );
  let author = $derived(
    server?.authMode === "none" ||
      project?.allowedActions?.includes("template.create"),
  );
  let operator = $derived(
    project?.allowedActions?.includes("instance.batchCreate") ?? false,
  );
  let manager = $derived(
    project?.allowedActions?.includes("quota.manage") ?? false,
  );
  let target = $derived(targets.find((t) => t.id === targetId));
  let docker = $derived(target?.providerKind === "docker");
  let detail = $derived(instances.find((i) => i.id === detailId));
  let visibleInstances = $derived(
    instances.filter(
      (i) =>
        (showEnded || i.phase !== "destroyed") &&
        (view === "classroom" ||
          i.ownerId === session?.subjectId ||
          i.ownerKind !== "user") &&
        labName(i).toLowerCase().includes(search.toLowerCase()),
    ),
  );
  let allowed = $derived(
    (view === "workspaces" || view === "people") && server?.authMode === "none"
      ? false
      : view === "templates"
        ? !!author
        : view === "classroom"
          ? operator
          : view === "people" ||
              view === "workspaces" ||
              view === "operations" ||
              view === "plugins"
            ? administrator
            : view === "settings"
              ? manager
              : true,
  );

  onMount(() => {
    void bootstrap();
    return () => {
      disposed = true;
      generation++;
      selection++;
      clearTimeout(refreshTimer);
    };
  });
  let navigationGeneration = 0;
  afterNavigate(({ from }) => {
    if (!from) return;
    drawerOpen = false;
    void navigate(++navigationGeneration);
  });
  async function navigate(current: number) {
    if (!session) return;
    try {
      const next = await api<Session>("/session");
      if (disposed || current !== navigationGeneration) return;
      const identityChanged =
        next.subjectId !== session?.subjectId ||
        JSON.stringify(next.roles) !== JSON.stringify(session?.roles);
      session = next;
      if (identityChanged) {
        loading = true;
        generation++;
        instances = [];
        roster = [];
        projects = [];
        try {
          await loadProjects();
        } finally {
          if (!disposed && current === navigationGeneration) loading = false;
        }
      } else {
        await loadProject(false);
      }
    } catch (cause) {
      if (!disposed && current === navigationGeneration) failure(cause);
    }
  }
  function failure(cause: unknown) {
    if (cause instanceof ApiError && cause.status === 401) {
      generation++;
      selection++;
      session = undefined;
      loading = false;
      instances = [];
      roster = [];
      clearTimeout(refreshTimer);
    }
    error =
      cause instanceof Error
        ? cause.message
        : "Something went wrong. Please try again.";
  }
  async function action(work: () => Promise<void>) {
    if (busy) return;
    busy = true;
    error = "";
    notice = "";
    try {
      await work();
    } catch (cause) {
      failure(cause);
    } finally {
      busy = false;
    }
  }
  async function bootstrap() {
    loading = true;
    error = "";
    try {
      server = await api<ServerInfo>("/server-info");
      try {
        session = await api<Session>("/session");
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 401) return;
        throw cause;
      }
      await loadProjects();
    } catch (cause) {
      failure(cause);
    } finally {
      loading = false;
    }
  }
  async function loadProjects() {
    projects =
      server?.authMode === "none"
        ? [
            {
              id: "project.local",
              organizationId: "organization.local",
              name: "Local workspace",
              allowedActions: ["template.create"],
            },
          ]
        : await collection<Project>("/projects", "after");
    const saved = localStorage.getItem(
      `labcentral.selected-project.v1.${session?.subjectId}`,
    );
    projectId =
      projects.find((p) => p.id === saved)?.id ?? projects[0]?.id ?? "";
    if (projectId && !globalView) await loadProject();
  }
  async function reloadProjects() {
    projects = await collection<Project>("/projects", "after", { fresh: true });
    if (!projects.some((p) => p.id === projectId))
      projectId = projects[0]?.id ?? "";
  }
  async function login() {
    await action(async () => {
      session = await api<Session>("/auth/local/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      password = "";
      await loadProjects();
    });
  }
  async function logout() {
    await action(async () => {
      await api("/auth/logout", { method: "POST" });
      session = undefined;
      projects = [];
      instances = [];
      roster = [];
      clearTimeout(refreshTimer);
    });
  }
  async function loadProject(reset = true) {
    if (globalView) {
      clearTimeout(refreshTimer);
      return;
    }
    const current = ++generation;
    stale = false;
    selection++;
    clearTimeout(refreshTimer);
    if (reset) loading = true;
    error = "";
    if (reset) {
      content = undefined;
      templateId = "";
      revisionId = "";
      revisions = [];
      selectedOwners = [];
      batch = undefined;
      activeOperation = undefined;
      instances = [];
      templates = [];
      roster = [];
    }
    localStorage.setItem(
      `labcentral.selected-project.v1.${session?.subjectId}`,
      projectId,
    );
    try {
      const [newTargets, newTemplates, labs] = await Promise.all([
        collection<ProviderTarget>(
          `/provider-targets?projectId=${encodeURIComponent(projectId)}`,
        ),
        collection<LabTemplate>(
          `/lab-templates?projectId=${encodeURIComponent(projectId)}`,
        ),
        collection<Instance>("/lab-instances"),
      ]);
      if (disposed || current !== generation) return;
      targets = newTargets;
      templates = newTemplates;
      const nextRevisionTemplates: Record<string, LabTemplate> = {};
      const metadata = await Promise.allSettled(
        newTemplates.map(async (t) => ({
          template: t,
          revisions: await collection<Revision>(
            `/lab-templates/${t.id}/revisions?projectId=${encodeURIComponent(projectId)}`,
          ),
        })),
      );
      if (disposed || current !== generation) return;
      if (metadata.some((result) => result.status === "rejected"))
        notice =
          "Some template information could not load. Your lab states are still available.";
      for (const result of metadata)
        if (result.status === "fulfilled")
          for (const r of result.value.revisions)
            nextRevisionTemplates[r.id] = result.value.template;
      revisionTemplates = nextRevisionTemplates;
      instances = labs.filter((i) => i.projectId === projectId);
      if (reset || !targets.some((t) => t.id === targetId))
        targetId =
          targets.find((t) => t.id === server?.defaultProviderTargetId)?.id ??
          targets[0]?.id ??
          "";
      if (reset) {
        source = targets.find((t) => t.id === targetId)?.example?.source ?? "";
        providerOverride = targets.find((t) => t.id === targetId)?.example
          ?.providerOverride
          ? JSON.stringify(
              targets.find((t) => t.id === targetId)?.example?.providerOverride,
              null,
              2,
            )
          : "";
      }
      if (
        !templates.length &&
        server?.authMode === "none" &&
        view !== "templates"
      ) {
        if (!source)
          source = (await api<{ source: string }>("/examples/docker-laptop"))
            .source;
        const now = new Date().toISOString();
        templates = [
          {
            id: "builtin.local",
            projectId,
            name: "Docker starter",
            description:
              "Start a two-container web lab in your trusted local environment.",
            resourceVersion: 1,
            createdBy: session?.subjectId ?? "",
            createdAt: now,
            updatedAt: now,
          },
        ];
      }
      if (
        !templateId &&
        (view === "launch" || view === "classroom" || view === "templates")
      ) {
        templateId = templates[0]?.id ?? "";
        if (templateId) await chooseTemplate();
      }
      if (view === "classroom" && operator && targetId) await loadRoster(reset);
      scheduleRefresh();
    } catch (cause) {
      if (current === generation) failure(cause);
    } finally {
      if (current === generation) loading = false;
    }
  }
  async function loadRoster(resetSelection = true) {
    const current = generation;
    const selectedTarget = targetId;
    const people = await collection<Person>(
      `/projects/${encodeURIComponent(projectId)}/members?targetId=${encodeURIComponent(targetId)}`,
      "after",
    );
    if (disposed || current !== generation || selectedTarget !== targetId)
      return;
    roster = people;
    if (resetSelection) selectedOwners = [];
  }
  function scheduleRefresh() {
    clearTimeout(refreshTimer);
    if (!disposed && session)
      refreshTimer = setTimeout(() => {
        void refresh();
      }, 3000);
  }
  async function refresh(fresh = false) {
    const current = generation;
    try {
      const labs = await collection<Instance>("/lab-instances", "cursor", {
        fresh,
      });
      if (disposed || current !== generation) return;
      instances = labs.filter((i) => i.projectId === projectId);
      if (activeOperation && !terminal(activeOperation.state)) {
        const updated = await api<Operation>(
          `/operations/${activeOperation.id}`,
          { fresh },
        );
        if (disposed || current !== generation) return;
        activeOperation = updated;
      }
      if (batch) {
        const updated = await api<Batch>(`/lab-instance-batches/${batch.id}`, {
          fresh,
        });
        if (disposed || current !== generation) return;
        batch = updated;
      }
      stale = false;
    } catch (cause) {
      if (disposed || current !== generation) return;
      stale = true;
      if (cause instanceof ApiError && cause.status === 401) failure(cause);
    } finally {
      if (current === generation) scheduleRefresh();
    }
  }
  async function chooseTemplate() {
    const selected = ++selection;
    content = undefined;
    revisionId = "";
    revisions = [];
    inputs = [];
    acknowledge = false;
    if (templateId !== "builtin.local") providerOverride = "";
    if (!templateId) {
      source = "";
      return;
    }
    if (templateId === "builtin.local") {
      content = { source, parameters: [] };
      return;
    }
    try {
      const result = await collection<Revision>(
        `/lab-templates/${templateId}/revisions?projectId=${encodeURIComponent(projectId)}`,
      );
      if (selected !== selection || disposed) return;
      revisions = result.sort((a, b) => b.revisionNumber - a.revisionNumber);
      revisionId = revisions[0]?.id ?? "";
      templateName = template?.name ?? "";
      templateDescription = template?.description ?? "";
      if (revisionId) await chooseRevision();
    } catch (cause) {
      failure(cause);
    }
  }
  async function chooseRevision() {
    const selected = ++selection;
    content = undefined;
    inputs = [];
    if (!revisionId) return;
    try {
      const result = await api<TemplateContent>(
        `/lab-templates/${templateId}/revisions/${revisionId}/content?projectId=${encodeURIComponent(projectId)}`,
      );
      if (selected !== selection || disposed) return;
      content = result;
      source = result.source;
    } catch (cause) {
      failure(cause);
    }
  }
  function requestBody() {
    return {
      ...(revisionId ? { revisionId } : { source }),
      projectId,
      providerTargetId: targetId,
      inputs,
      acknowledgeDockerIsolationRisk: acknowledge,
      ...(providerOverride.trim()
        ? { providerOverride: JSON.parse(providerOverride) }
        : {}),
    };
  }
  async function launch() {
    await action(async () => {
      const response = await api<{ operation: Operation; instanceId?: string }>(
        "/lab-instances",
        {
          method: "POST",
          headers: mutationHeaders(),
          body: JSON.stringify({
            ...requestBody(),
            ownerKind: "user",
            ownerId: session?.subjectId,
          }),
        },
      );
      activeOperation = response.operation;
      notice = "Lab launch queued.";
      await refresh();
    });
  }
  async function launchClassroom() {
    await action(async () => {
      batch = await api<Batch>("/lab-instance-batches", {
        method: "POST",
        headers: mutationHeaders(),
        body: JSON.stringify({
          ...requestBody(),
          owners: selectedOwners.map((ownerId) => ({
            ownerKind: "user",
            ownerId,
          })),
        }),
      });
      notice = "Classroom launch accepted.";
      await refresh();
    });
  }
  async function validateTemplate() {
    await action(async () => {
      const result = await api<{
        resourceCount: number;
        warnings?: { message: string }[];
      }>("/lab-validations", {
        method: "POST",
        body: JSON.stringify({
          source,
          projectId,
          providerTargetId: targetId,
          inputs,
          acknowledgeDockerIsolationRisk: acknowledge,
        }),
      });
      notice =
        `Template validated: ${result.resourceCount} resources.` +
        (result.warnings ?? [])
          .map((warning) => ` ${warning.message}`)
          .join("");
    });
  }
  async function publish() {
    await action(async () => {
      if (!templateId) {
        const created = await api<LabTemplate>("/lab-templates", {
          method: "POST",
          headers: mutationHeaders(),
          body: JSON.stringify({
            projectId,
            name: templateName,
            description: templateDescription,
          }),
        });
        templates = [...templates, created];
        templateId = created.id;
      } else {
        const updated = await api<LabTemplate>(
          `/lab-templates/${templateId}?projectId=${encodeURIComponent(projectId)}`,
          {
            method: "PUT",
            headers: {
              ...mutationHeaders(),
              "If-Match": `"${template?.resourceVersion}"`,
            },
            body: JSON.stringify({
              name: templateName,
              description: templateDescription,
            }),
          },
        );
        templates = templates.map((t) => (t.id === updated.id ? updated : t));
      }
      await api(
        `/lab-templates/${templateId}/revisions?projectId=${encodeURIComponent(projectId)}`,
        {
          method: "POST",
          headers: mutationHeaders(),
          body: JSON.stringify({ source }),
        },
      );
      await chooseTemplate();
      notice = "Revision published. Students can now launch this lab.";
    });
  }
  async function endLab() {
    if (!ending) return;
    const instance = ending;
    await action(async () => {
      const result = await api<{ operation: Operation }>(
        `/lab-instances/${instance.id}/actions/destroy`,
        { method: "POST", headers: mutationHeaders(), body: "{}" },
      );
      activeOperation = result.operation;
      endDialog?.close();
      ending = undefined;
      notice = "Your lab is ending. Its files will be deleted.";
      await refresh();
    });
  }
  async function openConsole(instance: Instance, resourceId: string) {
    await action(async () => {
      const launch = await api<AccessLaunch>("/access-sessions", {
        method: "POST",
        headers: mutationHeaders(),
        body: JSON.stringify({
          instanceId: instance.id,
          resourceId,
          method: "vnc",
          durationSeconds: 1800,
        }),
      });
      const expected = `/access/${encodeURIComponent(launch.session.id)}`;
      const destination = new URL(launch.launchUrl, window.location.origin);
      if (
        !launch.oneTimeToken ||
        destination.origin !== window.location.origin ||
        destination.pathname !== expected ||
        destination.search ||
        destination.hash ||
        destination.username ||
        destination.password
      )
        throw new Error("The console launch was incomplete. Please try again.");
      sessionStorage.setItem(
        `labcentral.access-token.v1.${launch.session.id}`,
        launch.oneTimeToken,
      );
      window.location.assign(expected);
    });
  }
  function labName(instance: Instance) {
    return (
      ((instance.revisionId &&
        revisionTemplates[instance.revisionId]?.name.replaceAll("-", " ")) ||
        instance.resources
          ?.find((r) => r.kind === "VM")
          ?.id?.replaceAll("-", " ")) ??
      "Lab workspace"
    );
  }
  function expiry(instance: Instance) {
    return instance.expiresAt
      ? new Date(instance.expiresAt).toLocaleString([], {
          month: "short",
          day: "numeric",
          hour: "2-digit",
          minute: "2-digit",
        })
      : "No scheduled expiry";
  }
  function confirmEnd(instance: Instance) {
    ending = instance;
    endDialog?.showModal();
  }
</script>

<svelte:head><title>{titles[view]} · LabCentral</title></svelte:head>
{#if !session}
  <main class="grid min-h-dvh place-items-center px-6 py-12">
    <section class="w-full max-w-sm">
      <div class="mb-10 flex items-center gap-3">
        <FlaskConical class="text-primary" size={28} /><span
          class="text-xl font-semibold tracking-tight">LabCentral</span
        >
      </div>
      {#if loading}<p class="flex items-center gap-3 text-sm" role="status">
          <span class="loading loading-spinner loading-sm"></span>Opening your
          workspace…
        </p>
      {:else}
        <h1 class="text-2xl font-semibold tracking-tight">
          Sign in to LabCentral
        </h1>
        <p class="mt-3 text-sm leading-6 text-base-content/70">
          Sign in to your labs.
        </p>
        {#if error}<div class="alert alert-error alert-soft mt-6" role="alert">
            <CircleAlert size={18} /><span>{error}</span>
          </div>{/if}
        {#if server?.authMode === "oidc"}
          <a class="btn btn-primary mt-8 w-full" href="/api/v1/auth/oidc/begin"
            ><FingerprintPattern size={18} />Continue with your identity
            provider</a
          >
          <p class="mt-4 text-sm leading-6 text-base-content/70">
            Use your organization's account. If you need access, contact your
            instructor.
          </p>
        {:else if server?.authMode === "local"}
          <form
            class="mt-7 grid gap-5"
            onsubmit={(e) => {
              e.preventDefault();
              void login();
            }}
          >
            <label class="grid gap-2 text-sm"
              >Username<input
                class="input w-full"
                autocomplete="username"
                bind:value={username}
                required
              /></label
            >
            <label class="grid gap-2 text-sm"
              >Password<input
                class="input w-full"
                type="password"
                autocomplete="current-password"
                bind:value={password}
                required
              /></label
            >
            <button class="btn btn-primary w-full" disabled={busy}
              >{busy ? "Signing in…" : "Sign in"}</button
            >
          </form>
        {:else}<button class="btn mt-6" onclick={bootstrap}>Try again</button
          >{/if}
      {/if}
    </section>
  </main>
{:else}
  <div class="drawer lg:drawer-open min-h-dvh">
    <input
      id="workspace-navigation"
      type="checkbox"
      class="drawer-toggle"
      bind:checked={drawerOpen}
      aria-label="Navigation"
    />
    <div class="drawer-content min-w-0">
      <header
        class="flex min-h-20 flex-wrap items-center justify-between gap-3 border-b border-base-300 px-5 py-4 lg:px-10"
      >
        <div class="flex min-w-0 items-center gap-3">
          <button
            class="btn btn-square btn-ghost lg:hidden"
            aria-label="Open navigation"
            onclick={() => (drawerOpen = true)}><Menu size={20} /></button
          >{#if globalView}<span class="text-sm text-base-content/70"
              >Administration</span
            >{:else}<span class="text-sm text-base-content/70">Workspace</span
            ><ChevronRight size={14} class="text-base-content/50" /><label
              class="sr-only"
              for="project-choice">Project</label
            ><select
              id="project-choice"
              class="select select-ghost max-w-52 font-medium"
              bind:value={projectId}
              onchange={() => void loadProject()}
              disabled={loading}
              >{#each projects as p}<option value={p.id}>{p.name}</option
                >{/each}</select
            >{/if}
        </div>
        <div class="flex items-center gap-2 text-sm">
          <span class="hidden text-base-content/60 sm:inline">Signed in as</span
          ><span class="font-medium">{session.displayName}</span>
        </div>
      </header>
      <main
        id="main-content"
        class="mx-auto max-w-7xl px-5 py-8 lg:px-10 lg:py-10"
      >
        {#if server?.authMode === "none"}<div
            class="alert alert-warning alert-soft mb-6"
            role="alert"
          >
            <ShieldCheck size={20} />
            <div>
              <strong>No-auth trusted-laptop mode</strong>
              <p>{server.noAuthWarning}</p>
            </div>
          </div>{/if}
        <div class="mb-8 flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 class="text-3xl font-semibold tracking-tight">
              {titles[view]}
            </h1>
          </div>
          {#if view === "labs"}<a href="/launch/" class="btn btn-primary"
              ><Plus size={17} />Launch lab</a
            >{/if}
        </div>
        {#if error}<div role="alert" class="alert alert-error alert-soft mb-6">
            <CircleAlert size={18} /><span class="break-words">{error}</span
            ><button
              class="btn btn-sm btn-ghost"
              aria-label="Dismiss error"
              onclick={() => (error = "")}><X size={16} /></button
            >
          </div>{/if}
        {#if notice}<div
            role="status"
            class="alert alert-success alert-soft mb-6"
          >
            <span>{notice}</span>{#if view === "launch"}<a class="link" href="/"
                >View my labs</a
              >{/if}
          </div>{/if}
        {#if stale}<div
            role="status"
            class="alert alert-warning alert-soft mb-6"
          >
            <span>Updates are paused. Showing the last known state.</span
            ><button class="btn btn-sm" onclick={() => refresh(true)}
              >Retry</button
            >
          </div>{/if}
        {#if activeOperation}<div
            class="mb-6 flex flex-wrap items-center gap-3 rounded-box border border-base-300 p-4"
            role="status"
          >
            <StatusBadge state={activeOperation.state} /><span class="text-sm"
              >{activeOperation.errorCode
                ? "This operation needs attention. Open the lab for details."
                : "Updating your lab…"}</span
            >
          </div>{/if}
        {#if loading}<div
            class="grid gap-4 sm:grid-cols-2"
            role="status"
            aria-label="Loading workspace"
          >
            <div class="skeleton h-48"></div>
            <div class="skeleton h-48"></div>
          </div>
        {:else if !projectId && !globalView}<section
            class="rounded-box border border-base-300 p-8"
          >
            <BookOpen size={28} class="mb-4 text-base-content/60" />
            <h2 class="text-lg font-semibold">No projects assigned yet</h2>
            <p class="mt-2 max-w-lg text-sm leading-6 text-base-content/70">
              Ask your administrator to add you to a classroom. Your projects
              will appear here automatically.
            </p>
            <button class="btn mt-5" onclick={bootstrap}
              >Check for access</button
            >
          </section>
        {:else if !allowed}<section
            class="rounded-box border border-base-300 p-8"
          >
            <ShieldCheck size={28} class="mb-4" />
            <h2 class="text-lg font-semibold">
              This view needs additional access
            </h2>
            <p class="mt-2 text-sm text-base-content/70">
              {view === "classroom"
                ? "Classroom launches require instructor access and an environment granted to this workspace. Choose an enabled workspace or ask an administrator to configure access."
                : "Choose another project or return to your labs."}
            </p>
            <div class="mt-5 flex flex-wrap gap-3">
              <a href="/" class="btn">My labs</a
              >{#if administrator && view === "classroom"}<a
                  href="/plugins/"
                  class="btn">Configure environment access</a
                >{/if}
            </div>
          </section>
        {:else if view === "workspaces"}
          <Workspaces
            {projects}
            onchange={reloadProjects}
            onselect={async (id) => {
              projectId = id;
              localStorage.setItem(
                `labcentral.selected-project.v1.${session?.subjectId}`,
                id,
              );
            }}
          />
        {:else if view === "plugins"}
          <Plugins
            {projects}
            authMode={server?.authMode ?? ""}
            onchange={reloadProjects}
          />
        {:else if view === "labs"}
          <div class="mb-5 flex flex-wrap items-center justify-between gap-4">
            <label class="input w-full sm:w-80"
              ><Search size={16} /><input
                type="search"
                aria-label="Search labs"
                placeholder="Search your labs…"
                bind:value={search}
              /></label
            >
            <div class="flex items-center gap-4">
              <label
                class="flex items-center gap-2 text-sm text-base-content/70"
                ><input
                  class="checkbox checkbox-sm"
                  type="checkbox"
                  bind:checked={showEnded}
                />Show ended labs</label
              ><button
                class="btn btn-sm btn-ghost"
                onclick={() => refresh(true)}
                aria-label="Refresh labs"><RefreshCw size={16} /></button
              >
            </div>
          </div>
          {#if !visibleInstances.length}<section
              class="grid justify-items-center rounded-box border border-dashed border-base-300 px-6 py-20 text-center"
            >
              <div class="mb-5 rounded-box bg-base-200 p-4">
                <FlaskConical size={30} class="text-primary" />
              </div>
              <h2 class="text-xl font-semibold">
                {search ? "No matching labs" : "Your next lab starts here"}
              </h2>
              <p class="mt-2 max-w-sm text-sm leading-6 text-base-content/70">
                {search
                  ? "Try a different search or include ended labs."
                  : "Launch a lab from your classroom’s templates. Your own environment will appear here."}
              </p>
              {#if !search}<a href="/launch/" class="btn mt-6"
                  ><Plus size={16} />Browse labs</a
                >{/if}
            </section>
          {:else}{#key projectId}<LabList
                instances={visibleInstances}
                name={labName}
                {refresh}
              />{/key}{/if}
        {:else if view === "operations"}
          <Operations {projects} name={labName} />
        {:else if view === "detail"}
          <LabDetail
            {detail}
            name={detail ? labName(detail) : ""}
            expires={detail ? expiry(detail) : ""}
            instructions={detail?.revisionId
              ? revisionTemplates[detail.revisionId]?.description
              : undefined}
            {administrator}
            browserConsole={server?.uiFeatures?.includes("browser-vnc") ??
              false}
            {busy}
            onopen={openConsole}
            onend={confirmEnd}
          />
        {:else if view === "launch" || view === "classroom"}
          {#if !templates.length}<section
              class="rounded-box border border-base-300 p-8"
            >
              <BookOpen size={28} class="mb-4" />
              <h2 class="text-lg font-semibold">No published labs yet</h2>
              <p class="mt-2 text-sm text-base-content/70">
                Your instructor needs to publish a lab template for this
                project.
              </p>
              {#if author}<a href="/templates/" class="btn mt-5"
                  >Create a template</a
                >{/if}
            </section>
          {:else}<div class="grid gap-7 xl:grid-cols-[minmax(0,1fr)_20rem]">
              <section class="min-w-0 rounded-box border border-base-300 p-6">
                <label class="grid gap-2 text-sm font-medium"
                  >Lab template<select
                    class="select w-full"
                    bind:value={templateId}
                    onchange={chooseTemplate}
                    >{#each templates as t}<option value={t.id}>{t.name}</option
                      >{/each}</select
                  ></label
                >
                <p
                  class="mt-4 whitespace-pre-wrap text-sm leading-6 text-base-content/70"
                >
                  {template?.description}
                </p>
                {#if templateId !== "builtin.local"}<label
                    class="mt-5 grid gap-2 text-sm"
                    >Revision<select
                      class="select w-full"
                      bind:value={revisionId}
                      onchange={chooseRevision}
                      >{#each revisions as r}<option value={r.id}
                          >Revision {r.revisionNumber}</option
                        >{/each}</select
                    ></label
                  >{/if}{#if !revisions.length && templateId !== "builtin.local"}<p
                    class="mt-4 text-sm text-warning"
                  >
                    This template has no published revision.
                  </p>{/if}{#key revisionId}{#if content}<DeclaredInputs
                      parameters={content.parameters ?? []}
                      bind:values={inputs}
                      bind:valid={inputsValid}
                    />{/if}{/key}{#if targets.length > 1}<label
                    class="mt-5 grid gap-2 text-sm"
                    >Environment<select
                      class="select w-full"
                      bind:value={targetId}
                      onchange={() => {
                        acknowledge = false;
                        if (view === "classroom") void action(loadRoster);
                      }}
                      >{#each targets as t}<option value={t.id}
                          >{t.providerKind} · {t.id}</option
                        >{/each}</select
                    ></label
                  >{/if}{#if docker}<label
                    class="mt-5 flex items-start gap-3 text-sm"
                    ><input
                      class="checkbox checkbox-sm mt-1"
                      type="checkbox"
                      bind:checked={acknowledge}
                    /><span
                      >I understand this container environment is for trusted
                      code and does not isolate hostile workloads.</span
                    ></label
                  >{/if}
              </section>
              <aside class="rounded-box bg-base-200 p-6">
                <h2 class="font-semibold">Ready to launch</h2>
                <p class="mt-3 text-sm leading-6 text-base-content/70">
                  Labs expire automatically. Save your work before deleting
                  them.
                </p>

                {#if view === "launch"}<button
                    class="btn btn-primary mt-7 w-full"
                    disabled={busy ||
                      (!revisionId && templateId !== "builtin.local") ||
                      !content ||
                      !inputsValid ||
                      !targetId ||
                      (docker && !acknowledge)}
                    onclick={launch}
                    ><Play size={16} />{busy
                      ? "Launching…"
                      : "Launch lab"}</button
                  >{/if}
              </aside>
            </div>{/if}
          {#if view === "classroom"}<section class="mt-8 min-w-0">
              <div
                class="mb-4 flex flex-wrap items-center justify-between gap-3"
              >
                <h2 class="text-lg font-semibold">Classroom members</h2>
                <button
                  class="btn btn-primary"
                  disabled={busy ||
                    !selectedOwners.length ||
                    !revisionId ||
                    !content ||
                    !inputsValid ||
                    (docker && !acknowledge)}
                  onclick={launchClassroom}
                  ><Play size={16} />Launch for {selectedOwners.length} selected</button
                >
              </div>
              <div
                class="max-w-full overflow-x-auto rounded-box border border-base-300"
              >
                <table class="table table-sm">
                  <thead
                    ><tr
                      ><th>Select</th><th>Student</th><th>Labs</th><th
                        >Progress</th
                      ></tr
                    ></thead
                  ><tbody
                    >{#each roster as person}<tr
                        ><td
                          ><input
                            class="checkbox checkbox-sm"
                            type="checkbox"
                            value={person.id}
                            bind:group={selectedOwners}
                            aria-label={`Select ${person.displayName}`}
                          /></td
                        ><td class="font-medium"
                          >{person.displayName}
                          <div class="mt-1 text-xs text-base-content/60">
                            {person.username}
                          </div></td
                        ><td
                          >{instances.filter(
                            (i) =>
                              i.ownerId === person.id &&
                              i.phase !== "destroyed",
                          ).length}</td
                        ><td
                          >{#each instances.filter((i) => i.ownerId === person.id && i.phase !== "destroyed") as instance}<a
                              href={`/lab/?id=${instance.id}`}
                              class="mr-2 inline-flex"
                              ><StatusBadge state={instance.phase} /></a
                            >{/each}{#each batch?.items?.filter((i) => i.ownerId === person.id) ?? [] as item}<StatusBadge
                              state={item.operation.state}
                            />{/each}</td
                        ></tr
                      >{/each}</tbody
                  >
                </table>
              </div>
              {#if !roster.length}<p class="mt-4 text-sm text-base-content/70">
                  No eligible students are assigned to this project and
                  environment.
                </p>{/if}
            </section>
            <section class="mt-8 min-w-0">
              <h2 class="mb-4 text-lg font-semibold">Classroom labs</h2>
              {#key projectId}<LabList
                  instances={instances.filter((i) => i.phase !== "destroyed")}
                  name={labName}
                  owner={(i) =>
                    roster.find((p) => p.id === i.ownerId)?.displayName ??
                    i.ownerId}
                  {refresh}
                />{/key}
            </section>{/if}
        {:else if view === "templates"}
          <div class="grid gap-6 xl:grid-cols-[17rem_minmax(0,1fr)]">
            <aside>
              <button
                class="btn mb-4 w-full"
                onclick={() => {
                  selection++;
                  templateId = "";
                  revisionId = "";
                  templateName = "";
                  templateDescription = "";
                  source = target?.example?.source ?? "";
                  content = undefined;
                }}><Plus size={16} />New template</button
              >
              <ul class="menu w-full rounded-box border border-base-300">
                {#each templates as t}<li>
                    <button
                      class:menu-active={templateId === t.id}
                      onclick={() => {
                        templateId = t.id;
                        void chooseTemplate();
                      }}><BookOpen size={16} />{t.name}</button
                    >
                  </li>{/each}
              </ul>
            </aside>
            <form
              class="grid min-w-0 gap-5 rounded-box border border-base-300 p-6"
              onsubmit={(e) => {
                e.preventDefault();
                void publish();
              }}
            >
              <h2 class="text-lg font-semibold">
                {templateId
                  ? "Publish a new revision"
                  : "Create a lab template"}
              </h2>
              <label class="grid gap-2 text-sm"
                >Template name<input
                  class="input w-full"
                  bind:value={templateName}
                  required
                  pattern={"[a-z][a-z0-9-]{0,62}"}
                  placeholder="debian-starter"
                /></label
              ><label class="grid gap-2 text-sm"
                >Description and instructions<textarea
                  class="textarea h-28 w-full"
                  bind:value={templateDescription}
                  maxlength="4096"
                  placeholder="What will students learn, and how should they get started?"
                ></textarea></label
              ><label class="grid gap-2 text-sm"
                >Lab YAML<textarea
                  class="textarea h-80 w-full font-mono text-xs leading-6"
                  bind:value={source}
                  spellcheck="false"
                  required></textarea></label
              >
              <details class="rounded-box border border-base-300 p-4 text-sm">
                <summary class="cursor-pointer font-medium"
                  >Validate with example values</summary
                >
                <p class="mt-3 text-base-content/70">
                  Values below are only used for validation. Define student
                  inputs in the YAML parameters.
                </p>
                <ParameterInputs
                  bind:values={inputs}
                  bind:valid={inputsValid}
                />
                <label class="mt-4 grid gap-2"
                  >Validation environment<select
                    class="select w-full"
                    bind:value={targetId}
                  >
                    {#each targets as t}<option value={t.id}
                        >{t.providerKind} · {t.id}</option
                      >{/each}
                  </select></label
                >
                {#if docker}<label class="mt-4 flex items-start gap-3"
                    ><input
                      type="checkbox"
                      class="checkbox checkbox-sm"
                      bind:checked={acknowledge}
                    />I understand this container environment is for trusted
                    code.</label
                  >{/if}
                <button
                  type="button"
                  class="btn mt-4"
                  disabled={busy ||
                    !source ||
                    !targetId ||
                    !inputsValid ||
                    (docker && !acknowledge)}
                  onclick={validateTemplate}>Validate template</button
                >
              </details>
              <div class="flex flex-wrap items-center justify-between gap-3">
                <p class="text-sm text-base-content/70">
                  Published revisions are immutable.
                </p>
                <button class="btn btn-primary" disabled={busy || !source}
                  >{busy ? "Publishing…" : "Publish revision"}</button
                >
              </div>
            </form>
          </div>
        {:else if view === "people"}{#key projectId}<People
              {project}
              authMode={server?.authMode ?? ""}
            />{/key}
        {:else if view === "settings"}{#key projectId}<Policies
              {projectId}
              organizationId={project?.organizationId ?? ""}
              {targets}
            />{/key}{/if}
      </main>
    </div>
    <div class="drawer-side z-30">
      <label
        for="workspace-navigation"
        class="drawer-overlay"
        aria-label="Close navigation"
      ></label>
      <aside
        class="flex min-h-dvh w-60 flex-col border-r border-base-300 bg-base-200 p-4"
      >
        <a href="/" class="mb-9 flex items-center gap-3 px-3 py-3"
          ><FlaskConical size={25} class="text-primary" /><span
            class="text-lg font-semibold tracking-tight">LabCentral</span
          ></a
        >
        <Navigation
          {view}
          {author}
          {manager}
          {administrator}
          authenticated={server?.authMode !== "none"}
        />
        <div class="mt-auto px-3 pt-16">
          {#if server?.authMode !== "none"}<button
              class="btn btn-sm btn-ghost w-full justify-start"
              onclick={logout}
              disabled={busy}><LogOut size={16} />Sign out</button
            >{/if}
        </div>
      </aside>
    </div>
  </div>
  <dialog bind:this={endDialog} class="modal" aria-labelledby="end-title">
    <div class="modal-box">
      <h2 id="end-title" class="text-xl font-semibold">End this lab?</h2>
      <p class="mt-4 text-sm leading-6 text-base-content/70">
        This deletes the lab and all files inside it. You can launch a fresh
        environment later, but this work cannot be recovered.
      </p>
      <div class="modal-action">
        <form method="dialog">
          <button class="btn" disabled={busy}>Keep lab</button>
        </form>
        <button class="btn btn-error" disabled={busy} onclick={endLab}
          >{busy ? "Ending…" : "Delete lab and files"}</button
        >
      </div>
    </div>
  </dialog>
{/if}
