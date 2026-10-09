<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import TemplateExplorer from "$lib/TemplateExplorer.svelte";
  import {
    ArrowRight,
    BookOpen,
    Clock,
    Search,
    Users,
    RotateCw,
    Square,
    ListChecks,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Quota from "$lib/Quota.svelte";
  import Status from "$lib/Status.svelte";
  import EmptyState from "$lib/EmptyState.svelte";
  import { session, api, APIError, refresh } from "$lib/api";
  import {
    sessionScope,
    learnPath,
    endpoint,
    modeLabel,
    liveRun,
    transitional,
    runLabel,
  } from "$lib/learning";
  import type { LearningCatalog, PracticeRun } from "$lib/types";
  let mounted = $state(false);
  onMount(() => { mounted = true; });
  const practice = $derived(browser && mounted && page.url.searchParams.get("view") === "practice");
  let catalog = $state<LearningCatalog | null>(null);
  let failure = $state("");
  let error = $state("");
  let busy = $state(false);
  let query = $state("");
  let mode = $state("all");
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
        (mode === "all" || lab.mode === mode) &&
        (!query.trim() ||
          `${lab.title} ${lab.description}`
            .toLowerCase()
            .includes(query.trim().toLowerCase())),
    ),
  );
  const resume = $derived(
    labs.find((lab) => lab.completedSteps > 0 && lab.completedSteps < lab.stepCount),
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
<PageHeader
  title={practice ? "Practice labs" : "Lab templates"}
  description={practice ? "Published labs stay pinned to one template revision. Run them in a personal cloud desktop or on your own infrastructure." : "Explore lab environments and their authored stages, pinned to a source revision."}
/>
<Guard>
  <nav class="tabs tabs-border mb-6 flex-nowrap" aria-label="Lab catalog views">
    <a class="tab min-h-11" class:tab-active={!practice} aria-current={!practice ? "page" : undefined} href="/labs/">Browse templates</a>
    <a class="tab min-h-11" class:tab-active={practice} aria-current={practice ? "page" : undefined} href="/labs/?view=practice">Practice labs</a>
  </nav>
  {#if !practice}<TemplateExplorer labs={catalog?.labs ?? null} practiceError={failure} retryPractice={load} />{:else}
  <Notice message={error} dismissible />
  {#if failure && catalog}<p class="mb-4 text-sm text-warning" role="status">
      Catalog updates are unavailable ({failure}). Showing the last loaded
      list.
    </p>{/if}
  {#if meetingOpen && $session?.meeting}<div
      class="mb-6 flex flex-wrap items-center gap-x-4 gap-y-2 border-y border-base-content/10 py-3 text-sm"
    >
      <Users size={16} class="shrink-0 text-base-content/75" aria-hidden="true" />
      <p class="min-w-0 flex-1">
        <span class="font-medium">{$session.meeting.name}</span>
        <span class="text-base-content/75">is open for team practice.</span>
      </p>
      <a
        class="btn btn-sm min-h-11 lg:min-h-8"
        href={$session.membership.teamID ? "/lab/" : "/teams/"}
        >{$session.membership.teamID ? "Your lab" : "Choose your team"}<ArrowRight
          size={15}
        /></a
      >
    </div>{/if}
  {#if catalog}
    <div
      class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_19rem] lg:gap-12"
    >
      <section class="min-w-0" aria-label="Published labs">
        <div class="flex flex-wrap items-end gap-3">
          <div class="min-w-0 flex-1 basis-56">
            <label class="label mb-1.5 text-sm" for="lab-search"
              >Search labs</label
            >
            <label class="input input-sm min-h-11 w-full lg:min-h-9">
              <Search size={15} aria-hidden="true" class="opacity-70" />
              <input
                id="lab-search"
                type="search"
                bind:value={query}
                placeholder="Title or topic"
                autocomplete="off"
              />
            </label>
          </div>
          <fieldset class="min-w-0">
            <legend class="mb-1.5 text-sm">Pacing</legend>
            <div class="tabs tabs-box tabs-sm flex-nowrap">
              {#each [["all", "All"], ["self-paced", "Self-paced"], ["instructor", "Instructor-led"]] as [value, label]}<input
                  type="radio"
                  name="lab-mode"
                  class="tab min-h-9"
                  aria-label={label}
                  {value}
                  bind:group={mode}
                />{/each}
            </div>
          </fieldset>
        </div>
        <p class="mt-4 text-xs text-base-content/75" role="status">
          {visible.length === labs.length
            ? `${labs.length} published ${labs.length === 1 ? "lab" : "labs"}`
            : `${visible.length} of ${labs.length} labs shown`}
        </p>
        <ul
          class="mt-2 divide-y divide-base-content/10 border-y border-base-content/10"
        >
          {#each visible as lab (lab.id)}
            {@const done = lab.stepCount > 0 && lab.completedSteps >= lab.stepCount}
            <li
              class="grid gap-3 py-5 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-6"
            >
              <div class="min-w-0">
                <div
                  class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-base-content/75"
                >
                  <span class="badge badge-ghost badge-sm">{modeLabel(lab.mode)}</span>
                  <span class="inline-flex items-center gap-1.5"
                    ><Clock size={13} aria-hidden="true" />About {lab.estimatedMinutes}
                    min</span
                  >
                  <span class="inline-flex items-center gap-1.5"
                    ><ListChecks size={13} aria-hidden="true" />{lab.stepCount}
                    {lab.stepCount === 1 ? "stage" : "stages"}</span
                  >
                </div>
                <h2 class="mt-2 text-base font-semibold leading-snug">
                  <a class="link link-hover" href={learnPath(lab.id)}>{lab.title}</a>
                </h2>
                {#if lab.description}<p
                    class="mt-1 line-clamp-2 max-w-[68ch] text-sm leading-relaxed text-base-content/75"
                  >
                    {lab.description}
                  </p>{/if}
                <p class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
                  {#if done}<span>All {lab.stepCount} stages complete</span
                    >{:else if lab.completedSteps}<span
                      >{lab.completedSteps} of {lab.stepCount} stages complete</span
                    >{:else}<span class="text-base-content/75">Not started</span
                    >{/if}
                  {#if run?.labID === lab.id}<span class="inline-flex items-center gap-1.5"
                      >Cloud workspace: <Status
                        status={run.status}
                        label={runLabel(run.status)}
                      /></span
                    >{/if}
                </p>
              </div>
              <a
                class="btn btn-sm min-h-11 w-fit lg:min-h-8"
                href={learnPath(lab.id)}
                aria-label={`${done ? "Review" : lab.completedSteps ? "Continue" : "Open"} ${lab.title}`}
                >{done ? "Review" : lab.completedSteps ? "Continue" : "Open lab"}<ArrowRight
                  size={15}
                /></a
              >
            </li>
          {:else}
            <li class="py-8">
              {#if labs.length}
                <p class="text-sm font-medium">No labs match these filters.</p>
                <button
                  type="button"
                  class="btn btn-sm btn-ghost min-h-11 mt-3"
                  onclick={() => {
                    query = "";
                    mode = "all";
                  }}>Clear filters</button
                >
              {:else}
                <BookOpen size={22} class="text-base-content/75" aria-hidden="true" />
                <p class="mt-3 text-base font-semibold">No labs are published yet</p>
                <p class="mt-1 text-sm text-base-content/75">
                  {#if $session?.user.admin}Publish a lab from a template revision
                    in <a class="link" href="/admin/labs/">Lab publishing</a
                    >.{:else}Published labs appear here when an instructor
                    releases them.{/if}
                </p>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
      <aside class="min-w-0 space-y-6 lg:border-l lg:border-base-content/10 lg:pl-8">
        {#if resume}<section aria-labelledby="resume-title">
            <p
              id="resume-title"
              class="text-xs font-medium tracking-wide text-base-content/75 uppercase"
            >
              Continue where you left off
            </p>
            <p class="mt-2 text-sm font-medium">{resume.title}</p>
            <p class="mt-1 text-xs tabular-nums text-base-content/75">
              Stage {resume.completedSteps + 1} of {resume.stepCount}
            </p>
            <a
              class="btn btn-sm btn-primary min-h-11 mt-3 w-full"
              href={learnPath(resume.id)}>Resume lab<ArrowRight size={15} /></a
            >
          </section>{/if}
        <Quota quota={catalog.quota} />
        <section aria-labelledby="run-title" class="border-t border-base-content/10 pt-5">
          <p id="run-title" class="text-sm font-medium">Your cloud workspace</p>
          {#if run}
            <div class="mt-2 flex flex-wrap items-center gap-2 text-sm">
              <Status status={run.status} label={runLabel(run.status)} />
              <span class="text-base-content/75"
                >{runLab ? runLab.title : "Lab no longer listed"}</span
              >
            </div>
            {#if run.error}<p class="mt-2 break-words text-sm text-error">
                {run.error}
              </p>{/if}
            <div class="mt-3 flex flex-wrap gap-2">
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
                >{:else}<p class="text-xs text-base-content/75">
                  Cleanup is in progress. Quota stops counting when the provider
                  confirms removal.
                </p>{/if}
            </div>
          {:else}
            <p class="mt-2 text-sm text-base-content/75">
              None running. You can run one cloud workspace at a time; start it
              from a lab.
            </p>
          {/if}
        </section>
      </aside>
    </div>
  {:else if failure}
    <Notice message={failure} />
    <button class="btn btn-sm min-h-11" onclick={() => load()}
      ><RotateCw size={15} />Retry</button
    >
  {:else}
    <div role="status" aria-label="Loading labs" class="max-w-3xl space-y-6">
      {#each [0, 1, 2] as row (row)}<div class="space-y-2 border-b border-base-content/10 pb-5">
          <div class="skeleton h-3 w-40"></div>
          <div class="skeleton h-5 w-72 max-w-full"></div>
          <div class="skeleton h-3 w-full"></div>
        </div>{/each}
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
    <p class="mt-3 text-sm leading-relaxed">
      This removes the workspace machines. Files in the workspace are deleted
      once the provider confirms cleanup. Your stage progress is kept.
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
