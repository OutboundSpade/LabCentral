<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import TemplateExplorer from "$lib/TemplateExplorer.svelte";
  import {
    ArrowRight,
    BookOpen,
    Search,
    RotateCw,
    Square,
    Cloud,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Quota from "$lib/Quota.svelte";
  import Status from "$lib/Status.svelte";
  import { session, api, APIError, refresh } from "$lib/api";
  import {
    sessionScope,
    learnPath,
    endpoint,
    liveRun,
    transitional,
    runLabel,
  } from "$lib/learning";
  import type { LearningCatalog, PracticeRun } from "$lib/types";
  let mounted = $state(false);
  onMount(() => { mounted = true; });
  // Practice is the default view; templates are a secondary, browse-only view.
  const practice = $derived(
    !(
      browser &&
      mounted &&
      (page.url.searchParams.get("view") === "templates" ||
        page.url.searchParams.has("template") ||
        page.url.searchParams.has("q"))
    ),
  );
  let catalog = $state<LearningCatalog | null>(null);
  let failure = $state("");
  let error = $state("");
  let busy = $state(false);
  let query = $state("");
  let stopDialog: HTMLDialogElement;
  let scope = "";
  let seq = 0;
  async function load() {
    const s = scope;
    const n = ++seq;
    if (!s) return;
    try {
      const next = await api<LearningCatalog>("/api/learning");
      if (n !== seq || s !== scope) return;
      catalog = next;
      failure = "";
    } catch (e) {
      if (n !== seq || s !== scope) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        catalog = null;
        void refresh();
      }
      failure = (e as Error).message;
    }
  }
  $effect(() => {
    const s = sessionScope($session);
    untrack(() => {
      if (s === scope) return;
      scope = s;
      seq++;
      catalog = null;
      failure = "";
      error = "";
      if (s) void load();
    });
  });
  const run = $derived(liveRun(catalog?.run));
  const interval = $derived(transitional(run) ? 5000 : 30000);
  $effect(() => {
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, interval);
    return () => clearInterval(timer);
  });
  const labs = $derived(catalog?.labs ?? []);
  const visible = $derived(
    labs.filter(
      (lab) =>
        (!query.trim() ||
          `${lab.title} ${lab.description}`
            .toLowerCase()
            .includes(query.trim().toLowerCase())),
    ),
  );
  const runLab = $derived(labs.find((lab) => lab.id === run?.labID));
  const meetingOpen = $derived(
    !!$session?.meeting &&
      !["ended", "ending"].includes($session.meeting.status),
  );
  async function stop() {
    if (!run || busy) return;
    const s = scope;
    busy = true;
    error = "";
    try {
      const next = await api<PracticeRun>(endpoint(run.labID, "stop"), {});
      if (s === scope && catalog) catalog.run = next;
      stopDialog.close();
    } catch (e) {
      if (s === scope) error = (e as Error).message;
    } finally {
      busy = false;
      void load();
    }
  }
</script>

<svelte:head><title>{practice ? "Practice labs" : "Lab templates"} · LabCentral</title></svelte:head>
<PageHeader title={practice ? "Practice labs" : "Lab templates"}>
  {#if practice && catalog}<div class="w-48"><Quota quota={catalog.quota} /></div>{/if}
</PageHeader>
<Guard>
  <nav class="mb-6 flex gap-5 border-b border-base-content/8 text-sm" aria-label="Lab catalog views">
    <a class={["-mb-px flex min-h-11 items-center border-b-2", practice ? "border-primary font-medium" : "border-transparent text-base-content/70 hover:text-base-content"]} aria-current={practice ? "page" : undefined} href="/labs/">Labs</a>
    <a class={["-mb-px flex min-h-11 items-center border-b-2", !practice ? "border-primary font-medium" : "border-transparent text-base-content/70 hover:text-base-content"]} aria-current={!practice ? "page" : undefined} href="/labs/?view=templates">Templates</a>
  </nav>
  {#if !practice}<TemplateExplorer labs={catalog?.labs ?? null} practiceError={failure} retryPractice={load} />{:else}
  <Notice message={error} dismissible />
  {#if failure && catalog}<p class="mb-4 text-sm text-warning" role="status">
      Updates are unavailable ({failure}). Showing the last loaded list.
    </p>{/if}
  {#if meetingOpen && $session?.meeting}<div
      class="panel mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 text-sm"
    >
      <span class="size-2 shrink-0 rounded-full bg-success" aria-hidden="true"></span>
      <p class="min-w-0 flex-1">
        <span class="font-medium">{$session.meeting.name}</span>
        <span class="text-base-content/70">is in progress</span>
      </p>
      <a
        class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
        href={$session.membership.teamID ? "/lab/" : "/teams/"}
        >{$session.membership.teamID ? "Go to your lab" : "Choose your team"}<ArrowRight
          size={15}
        /></a
      >
    </div>{/if}
  {#if catalog}
    {#if run}<section
        aria-labelledby="run-title"
        class="panel mb-4 flex flex-wrap items-center gap-x-4 gap-y-3 px-4 py-3"
      >
        <Cloud size={17} class="shrink-0 text-base-content/70" aria-hidden="true" />
        <div class="min-w-0 flex-1 basis-56 text-sm">
          <p id="run-title" class="font-medium">
            {runLab ? runLab.title : "Cloud workspace"}
          </p>
          <p class="mt-0.5 flex flex-wrap items-center gap-2 text-base-content/70">
            <Status status={run.status} label={runLabel(run.status)} />
            {#if run.status === "ending"}<span>Quota stops when cleanup is confirmed.</span>{/if}
          </p>
          {#if run.error}<p class="mt-1 break-words text-error">{run.error}</p>{/if}
        </div>
        <div class="flex flex-wrap gap-2">
          {#if runLab}<a class="btn btn-sm min-h-11 lg:min-h-8" href={learnPath(runLab.id)}
              >Open lab</a
            >{/if}
          {#if run.status !== "ending"}<button
              type="button"
              class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
              disabled={busy}
              onclick={() => {
                error = "";
                stopDialog.showModal();
              }}><Square size={14} />Stop workspace</button
            >{/if}
        </div>
      </section>{/if}
    <section aria-label="Published labs">
      {#if labs.length > 1}<label class="input input-sm mb-5 min-h-11 w-full sm:max-w-xs lg:min-h-9">
          <Search size={15} aria-hidden="true" class="opacity-60" />
          <input
            id="lab-search"
            type="search"
            aria-label="Search labs"
            bind:value={query}
            placeholder="Search labs"
            autocomplete="off"
          />
        </label>{/if}
      {#if query.trim()}<p class="sr-only" role="status">
          {visible.length} of {labs.length} labs shown
        </p>{/if}
      <ul class="grid gap-3 sm:grid-cols-2">
        {#each visible as lab (lab.id)}
          {@const done = lab.stepCount > 0 && lab.completedSteps >= lab.stepCount}
          {@const verb = done ? "Review" : lab.completedSteps ? "Continue" : "Open"}
          <li class="panel flex min-w-0 flex-col p-5 transition-colors hover:border-base-content/20">
            <div class="flex items-start justify-between gap-3">
              <h2 class="text-base font-semibold leading-snug">
                <a class="hover:underline" href={learnPath(lab.id)}>{lab.title}</a>
              </h2>
              {#if run?.labID === lab.id}<Status status={run.status} label={runLabel(run.status)} />{:else if lab.mode === "instructor"}<span class="badge badge-sm badge-ghost shrink-0">Instructor-led</span>{/if}
            </div>
            {#if lab.description}<p
                class="mt-1.5 line-clamp-2 text-sm leading-relaxed text-base-content/70"
              >
                {lab.description}
              </p>{/if}
            <div class="mt-auto flex items-center gap-4 pt-5">
              <div class="min-w-0 flex-1 text-xs tabular-nums text-base-content/70">
                {#if lab.completedSteps}<p>
                    {done ? "Complete" : `${lab.completedSteps} of ${lab.stepCount} stages`}
                  </p>
                  <progress
                    class="progress progress-primary mt-1.5 h-1 w-full"
                    value={lab.completedSteps}
                    max={lab.stepCount || 1}
                    aria-hidden="true"
                  ></progress>{:else}<p>
                    {lab.stepCount} {lab.stepCount === 1 ? "stage" : "stages"} · {lab.estimatedMinutes} min
                  </p>{/if}
              </div>
              <a
                class={["btn btn-sm min-h-11 lg:min-h-8", lab.completedSteps && !done ? "btn-primary" : ""]}
                href={learnPath(lab.id)}
                aria-label={`${verb} ${lab.title}`}
                >{done ? "Review" : lab.completedSteps ? "Continue" : "Start"}<ArrowRight
                  size={15}
                /></a
              >
            </div>
          </li>
        {:else}
          <li class="py-10 text-center sm:col-span-2">
            {#if labs.length}
              <p class="text-sm font-medium">No labs match “{query.trim()}”.</p>
              <button
                type="button"
                class="btn btn-sm btn-ghost min-h-11 mt-3"
                onclick={() => (query = "")}>Clear search</button
              >
            {:else}
              <BookOpen size={22} class="mx-auto text-base-content/60" aria-hidden="true" />
              <p class="mt-3 text-base font-semibold">No labs yet</p>
              <p class="mt-1 text-sm text-base-content/70">
                {#if $session?.user.admin}Publish one from <a class="link" href="/admin/labs/">Published labs</a
                  >.{:else}Labs appear here when an instructor publishes them.{/if}
              </p>
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {:else if failure}
    <Notice message={failure} />
    <button class="btn btn-sm min-h-11" onclick={() => load()}
      ><RotateCw size={15} />Retry</button
    >
  {:else}
    <div role="status" aria-label="Loading labs" class="grid gap-3 sm:grid-cols-2">
      {#each [0, 1, 2, 3] as row (row)}<div class="skeleton h-36 rounded-box"></div>{/each}
      <span class="sr-only">Loading labs…</span>
    </div>
  {/if}
  {/if}
</Guard>
<dialog bind:this={stopDialog} class="modal" aria-labelledby="catalog-stop-title">
  <div class="modal-box">
    <h2 id="catalog-stop-title" class="text-lg font-semibold">
      Stop your cloud workspace?
    </h2>
    <p class="mt-3 text-sm leading-relaxed text-base-content/80">
      Workspace files are deleted. Your stage progress is kept.
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={busy}>Cancel</button>
      </form>
      <button class="btn btn-sm btn-error min-h-11" disabled={busy} onclick={stop}
        >{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}Stop
        and delete files</button
      >
    </div>
  </div>
</dialog>
