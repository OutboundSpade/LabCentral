<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import { templateName } from "$lib/display";
  import TemplateExplorer from "$lib/TemplateExplorer.svelte";
  import {
    ArrowRight,
    History,
    LibraryBig,
    RotateCw,
    Shuffle,
    Square,
    Cloud,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Quota from "$lib/Quota.svelte";
  import Status from "$lib/Status.svelte";
  import { session, api, APIError, refresh, date } from "$lib/api";
  import {
    sessionScope,
    learnPath,
    endpoint,
    liveRun,
    transitional,
    runLabel,
  } from "$lib/learning";
  import {
    attempts as ownAttempts,
    templatePath,
    type AttemptCatalog,
  } from "$lib/library";
  import type { PracticeRun } from "$lib/types";
  let mounted = $state(false);
  onMount(() => {
    mounted = true;
  });
  // The template library is the default view; attempt history is secondary.
  const history = $derived(
    browser &&
      mounted &&
      page.url.searchParams.get("view") === "attempts" &&
      !page.url.searchParams.has("template"),
  );
  let catalog = $state<AttemptCatalog | null>(null);
  let failure = $state("");
  let error = $state("");
  let busy = $state(false);
  let stopDialog: HTMLDialogElement;
  let scope = "";
  let seq = 0;
  async function load() {
    const s = scope;
    const n = ++seq;
    if (!s) return;
    try {
      const next = await api<AttemptCatalog>("/api/learning");
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
  const mine = $derived(ownAttempts(catalog));
  const detail = $derived(browser && mounted && page.url.searchParams.has("template"));
  const runLab = $derived(catalog?.labs.find((lab) => lab.id === run?.labID));
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
  const tab =
    "-mb-px flex min-h-11 items-center gap-2 border-b-2 focus-visible:outline-2 focus-visible:outline-primary";
</script>

<svelte:head
  ><title>{history ? "My attempts" : "Practice"} · LabCentral</title></svelte:head
>
{#if !detail}<PageHeader title="Practice">
  {#if catalog}<div class="w-48"><Quota quota={catalog.quota} /></div>{/if}
</PageHeader>{/if}
<Guard>
  {#if !detail}<nav class="mb-6 flex gap-5 border-b border-base-content/8 text-sm" aria-label="Library views">
    <a
      class={[tab, !history ? "border-primary font-medium" : "border-transparent text-base-content/70 hover:text-base-content"]}
      aria-current={!history ? "page" : undefined}
      href="/labs/"><LibraryBig size={15} aria-hidden="true" />Library</a
    >
    <a
      class={[tab, history ? "border-primary font-medium" : "border-transparent text-base-content/70 hover:text-base-content"]}
      aria-current={history ? "page" : undefined}
      href="/labs/?view=attempts"
      ><History size={15} aria-hidden="true" />My attempts{#if mine.length}<span
          class="badge badge-sm badge-ghost">{mine.length}</span
        >{/if}</a
    >
  </nav>{/if}
  <Notice message={error} dismissible />
  {#if meetingOpen && $session?.meeting}<div
        class="rounded-box border border-base-content/9 bg-base-200/70 mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 text-sm"
      >
        <span class="size-2 shrink-0 rounded-full bg-success" aria-hidden="true"></span>
        <p class="min-w-0 flex-1">
          <span class="font-medium">{$session.meeting.name}</span>
          <span class="text-base-content/70">is in progress</span>
        </p>
        <a
          class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
          href={$session.membership.teamID ? "/lab/" : "/teams/"}
          >{$session.membership.teamID ? "Your lab" : "Choose your team"}<ArrowRight
            size={15}
          /></a
        >
      </div>{/if}
    {#if run}<section
        aria-labelledby="run-title"
        class="rounded-box border border-base-content/9 bg-base-200/70 mb-4 flex flex-wrap items-center gap-x-4 gap-y-3 px-4 py-3"
      >
        <Cloud size={17} class="shrink-0 text-base-content/70" aria-hidden="true" />
        <div class="min-w-0 flex-1 basis-56 text-sm">
          <p id="run-title" class="font-medium">
            {runLab ? templateName(runLab.title) : "Workspace"}
          </p>
          <p class="mt-0.5 flex flex-wrap items-center gap-2 text-base-content/70">
            <Status status={run.status} label={runLabel(run.status)} />
            {#if run.status === "ending"}<span>Quota stops when cleanup is confirmed.</span>{/if}
          </p>
          {#if run.error}<p class="mt-1 break-words text-error">{run.error}</p>{/if}
        </div>
        <div class="flex flex-wrap gap-2">
          <a class="btn btn-sm min-h-11 lg:min-h-8" href={learnPath(run.labID)}>Open lab</a>
          {#if run.status !== "ending"}<button
              type="button"
              class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8"
              disabled={busy}
              onclick={() => {
                error = "";
                stopDialog.showModal();
              }}><Square size={14} />Stop workspace</button
            >{/if}
        </div>
      </section>{/if}
  {#if !history}
    <TemplateExplorer {catalog} catalogError={failure} retryCatalog={load} />
  {:else if catalog}
    {#if failure}<p class="mb-4 text-sm text-warning" role="status">
        Updates are unavailable ({failure}). Showing your last loaded attempts.
      </p>{/if}
    <section aria-label="My attempts">
      <ul class="grid gap-3 sm:grid-cols-2">
        {#each mine as lab (lab.id)}
          {@const done = lab.stepCount > 0 && lab.completedSteps >= lab.stepCount}
          {@const verb = done ? "Review" : "Continue"}
          <li class="card card-sm border border-base-content/9 bg-base-200/70 min-w-0">
            <div class="card-body">
              <div class="flex items-start justify-between gap-3">
                <h2 class="card-title break-words text-base leading-snug">
                  <a class="hover:underline" href={learnPath(lab.id)}>{templateName(lab.title)}</a>
                </h2>
                {#if run?.labID === lab.id}<Status
                    status={run.status}
                    label={runLabel(run.status)}
                  />{:else if lab.scenarioID}<span class="badge badge-sm badge-ghost shrink-0"
                    ><Shuffle size={12} aria-hidden="true" />Randomized</span
                  >{/if}
              </div>
              <p class="grow-0 text-xs text-base-content/70">
                Started {date(lab.publishedAt, { dateStyle: "medium", timeStyle: "short" })}
              </p>
              <div class="mt-auto pt-3 text-xs tabular-nums text-base-content/70">
                <p>
                  {done
                    ? "Complete"
                    : `${lab.completedSteps} of ${lab.stepCount} ${lab.stepCount === 1 ? "stage" : "stages"}`}
                </p>
                <progress
                  class="progress mt-1.5 h-1 w-full"
                  value={lab.completedSteps}
                  max={lab.stepCount || 1}
                  aria-hidden="true"
                ></progress>
              </div>
              <div class="card-actions items-center justify-between pt-1">
                <a
                  class="link link-hover inline-flex min-h-11 items-center text-xs text-base-content/70 lg:min-h-8"
                  href={templatePath(lab.template)}
                  aria-label={`Template revision for ${lab.title}`}>Template revision</a
                >
                <a
                  class={["btn btn-sm min-h-11 lg:min-h-8", done ? "" : "btn-primary"]}
                  href={learnPath(lab.id)}
                  aria-label={`${verb} ${templateName(lab.title)}, started ${date(lab.publishedAt, { dateStyle: "medium", timeStyle: "short" })}`}
                  >{verb}<ArrowRight size={15} /></a
                >
              </div>
            </div>
          </li>
        {:else}
          <li class="py-10 text-center sm:col-span-2">
            <History size={22} class="mx-auto text-base-content/60" aria-hidden="true" />
            <p class="mt-3 text-base font-semibold">No attempts yet</p>
            <p class="mt-1 text-sm text-base-content/70">
              Start baseline practice or a scenario from a template.
            </p>
            <a class="btn btn-sm min-h-11 mt-4" href="/labs/">Browse templates<ArrowRight size={15} /></a>
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
    <div role="status" aria-label="Loading attempts" class="grid gap-3 sm:grid-cols-2">
      {#each [0, 1] as row (row)}<div class="skeleton h-36 rounded-box"></div>{/each}
      <span class="sr-only">Loading attempts…</span>
    </div>
  {/if}
</Guard>
<dialog bind:this={stopDialog} class="modal" aria-labelledby="catalog-stop-title">
  <div class="modal-box">
    <h2 id="catalog-stop-title" class="text-lg font-semibold">
      Stop your workspace?
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
