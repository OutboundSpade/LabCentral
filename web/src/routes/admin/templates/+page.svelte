<script lang="ts">
  import { page } from "$app/state";
  import { goto, replaceState } from "$app/navigation";
  import { templateName } from "$lib/display";
  import Activity from "$lib/Activity.svelte";
  import { onMount } from "svelte";
  import {
    BookOpen,
    Hammer,
    Info,
    RefreshCw,
    RotateCw,
    Search,
    Trash2,
    TriangleAlert,
    Users,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import AdminNav from "$lib/AdminNav.svelte";
  import Status from "$lib/Status.svelte";
  import PollStatus from "$lib/PollStatus.svelte";
  import Notice from "$lib/Notice.svelte";
  import TemplateCard from "$lib/TemplateCard.svelte";
  import MeetingDialog from "$lib/MeetingDialog.svelte";
  import ImageDialog from "$lib/ImageDialog.svelte";
  import {
    buildable,
    buildLabel,
    jobActive,
    revisionJob,
    revisionState,
  } from "$lib/job";
  import {
    adminTemplatePath,
    legacyTemplatePath,
    type LibraryTemplate,
  } from "$lib/library";
  import type { Job, Template } from "$lib/types";
  import { api, session, adminView, refreshAdmin, watchAdmin } from "$lib/api";
  let data = $derived($adminView);
  let mounted = $state(false);
  let error = $state("");
  let busy = $state(false);
  let catalog = $state<LibraryTemplate[] | null>(null);
  let catalogError = $state("");
  let meetingDialog: MeetingDialog;
  let imageDialog: ImageDialog;
  const refreshing = $derived(
    data?.jobs.some((j) => j.kind === "refresh" && jobActive(j)) ?? false,
  );
  const pruning = $derived(
    data?.jobs.some((j) => j.kind === "prune-images" && jobActive(j)) ?? false,
  );
  const pruneAttention = $derived(
    data?.jobs.some((j) => j.kind === "prune-images" && j.needsAttention) ?? false,
  );
  const meetingOpen = $derived(!!data?.meeting && data.meeting.status !== "ended");

  // Earlier ?template=<revision> links resolve to /admin/templates/<name>/<revision>/.
  const legacy = $derived(mounted ? (page.url.searchParams.get("template") ?? "") : "");
  const legacyScope = $derived($session?.user.admin ? $session.user.id : "");
  let legacyError = $state("");
  $effect(() => {
    const target = legacy;
    if (!target || !legacyScope) return;
    let cancelled = false;
    legacyError = "";
    legacyTemplatePath(target, "/admin/templates/")
      .then((path) => {
        if (!cancelled) void goto(path, { replaceState: true });
      })
      .catch((e) => {
        if (!cancelled) legacyError = (e as Error).message;
      });
    return () => {
      cancelled = true;
    };
  });

  // Image state of a template's newest revision, with build progress.
  type Card = {
    template: LibraryTemplate;
    revision?: Template;
    job?: Job | null;
    ready: boolean;
    state?: { status: string; label: string };
    progress?: { done: number; total: number };
  };
  function describe(template: LibraryTemplate): Card {
    const revision = data?.templates.find((x) => x.id === template.id);
    if (!revision) return { template, ready: false };
    const job = revisionJob(data, template.id);
    const base = revisionState(revision, job);
    const total = revision.imagesTotal;
    const done = Math.min(revision.imagesReady, total);
    const progress = total > 0 && done < total ? { done, total } : undefined;
    const count = `${done} of ${total} images`;
    let state = base;
    if (jobActive(job) && job?.kind === "build")
      state = { status: job.status, label: job.status === "queued" ? "Build queued" : `Building · ${count}` };
    else if (base.status === "failed") state = { status: "failed", label: `Needs attention · ${base.label.toLowerCase()}` };
    else if (base.label === "Not built" && done > 0) state = { status: "partial", label: count };
    return {
      template,
      revision,
      job,
      ready: revision.built && !revision.retired && !revision.removing,
      state,
      progress: jobActive(job) || state.status === "partial" ? progress : undefined,
    };
  }
  const cards = $derived(
    (catalog ?? [])
      .toSorted((a, b) => a.name.localeCompare(b.name))
      .map(describe),
  );

  // Client-side search and status filter, kept in the URL for back navigation.
  type Filter = "all" | "ready" | "unbuilt";
  let query = $state("");
  let filter = $state<Filter>("all");
  const filters: [Filter, string][] = [
    ["all", "All"],
    ["ready", "Ready"],
    ["unbuilt", "Not built"],
  ];
  const text = (c: Card) =>
    [
      c.template.name,
      templateName(c.template.name),
      c.template.description,
      c.template.library?.category,
      ...(c.template.library?.tags ?? []),
    ]
      .join(" ")
      .toLowerCase();
  const matching = $derived(
    cards.filter((c) => text(c).includes(query.trim().toLowerCase())),
  );
  const counts = $derived({
    all: matching.length,
    ready: matching.filter((c) => c.ready).length,
    unbuilt: matching.filter((c) => !c.ready).length,
  });
  const visible = $derived(
    matching.filter((c) => filter === "all" || (filter === "ready") === c.ready),
  );
  const filtered = $derived(!!query.trim() || filter !== "all");
  function syncURL() {
    const url = new URL(page.url);
    if (query.trim()) url.searchParams.set("q", query.trim());
    else url.searchParams.delete("q");
    if (filter !== "all") url.searchParams.set("status", filter);
    else url.searchParams.delete("status");
    replaceState(url, page.state);
  }
  function clearFilters() {
    query = "";
    filter = "all";
    syncURL();
  }

  async function loadCatalog() {
    try {
      // Only the newest visible revision of each template.
      catalog = (await api<{ templates: LibraryTemplate[] }>("/api/templates?latest=1")).templates;
      catalogError = "";
    } catch (e) {
      catalogError = (e as Error).message;
    }
  }
  // Reload card content only when revisions are imported, removed, superseded or
  // retired (head and retirement flags change without new IDs), not on every update.
  const revisions = $derived(
    data?.templates.map((t) => `${t.id}:${+t.latest}:${+t.retired}`).join(",") ?? null,
  );
  $effect(() => {
    // An empty installation still needs a catalog response to render its empty state.
    if (revisions !== null && !legacy) void loadCatalog();
  });
  onMount(() => {
    mounted = true;
    const params = page.url.searchParams;
    query = params.get("q") ?? "";
    const status = params.get("status");
    filter = status === "ready" || status === "unbuilt" ? status : "all";
    return watchAdmin();
  });
  async function refreshTemplates() {
    busy = true;
    error = "";
    try {
      await api("/api/admin/templates/refresh", {});
      await refreshAdmin().catch(() => {});
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
</script>

<svelte:head><title>Templates · LabCentral</title></svelte:head>
<PageHeader title="Templates">
  {#if $session?.user.admin}<button
      class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
      disabled={busy || refreshing}
      onclick={refreshTemplates}
      >{#if refreshing}<span class="loading loading-spinner loading-xs"></span>{:else}<RefreshCw
          size={15}
        />{/if}{refreshing ? "Refreshing…" : "Refresh from GitHub"}</button
    >{/if}
</PageHeader>
{#if $session?.user.admin}<AdminNav />{/if}
<Guard admin
  ><Notice message={error} dismissible /><PollStatus />
  {#if legacy}
    {#if legacyError}<Notice message={legacyError} /><a class="btn btn-sm min-h-11" href="/admin/templates/"
        >All templates</a
      >{:else}<p class="flex items-center gap-2 text-sm text-base-content/60" role="status">
        <span class="loading loading-spinner loading-sm"></span>Opening template revision…
      </p>{/if}
  {:else if data}
    {#if data.jobs.find((j) => j.kind === "refresh")?.status === "failed"}<Notice
        message={data.jobs.find((j) => j.kind === "refresh")?.error ??
          "GitHub refresh failed. Please retry."}
      />{/if}
    {#if data.importFailures?.length}<div role="alert" class="alert alert-soft alert-warning mb-6 items-start">
        <TriangleAlert size={18} aria-hidden="true" class="mt-0.5 shrink-0" />
        <div class="min-w-0">
          <p class="font-medium">
            {data.importFailures.length === 1
              ? "1 template was not imported"
              : `${data.importFailures.length} templates were not imported`}
          </p>
          <ul class="mt-2 space-y-1.5 text-sm">
            {#each data.importFailures as failure}<li class="break-words">
                <span class="font-medium">{failure.directory}</span>
                <code class="ml-1 text-xs text-base-content/80">{failure.error}</code>
              </li>{/each}
          </ul>
        </div>
      </div>{/if}
    <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-12">
      <section class="min-w-0" aria-label="Templates">
        <div class="mb-5 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
          {#if data.imageSummary}
            {@const summary = data.imageSummary}
            <details class="group min-w-0 flex-1 basis-72 text-sm">
              <summary
                class="flex min-h-11 cursor-pointer flex-wrap items-center gap-x-1.5 gap-y-1 rounded-field text-base-content/70 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary lg:min-h-8 [&::-webkit-details-marker]:hidden"
                ><span class="tabular-nums"
                  ><span class="font-medium text-base-content">{plural(summary.images, "cached image")}</span>
                  · used by {plural(summary.templates, "template")}{#if summary.partialTemplates}{` (${summary.partialTemplates} partially built)`}{/if}
                  · {summary.unusedImages} unused</span
                ><span class="inline-flex items-center gap-1 text-xs text-base-content/60 group-open:text-base-content"
                  ><Info size={14} aria-hidden="true" />How images are counted</span
                ></summary
              >
              <p class="mt-2 max-w-[65ch] text-xs leading-relaxed text-base-content/70">
                Images are cached per machine build and kept for the latest revision of every
                template, including templates that are only partly built. Templates that share a
                machine reuse the same image, so the image count can exceed the number of ready
                templates. Images no longer used by a latest revision, a meeting, practice or a
                build count as unused.
              </p>
            </details>
            {#if summary.unusedImages || pruning || pruneAttention}<button
                class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8"
                disabled={pruning || pruneAttention}
                title={pruneAttention ? "Retry cleanup in Live activity" : undefined}
                onclick={() =>
                  imageDialog.show("prune-images", null, data?.imageSummary?.unusedImages ?? 0)}
                ><Trash2 size={15} />{pruning
                  ? "Removing unused images…"
                  : "Remove unused images"}</button
              >{/if}
          {:else}<p class="text-sm text-base-content/70">Image inventory unavailable.</p>{/if}
        </div>
        {#if meetingOpen}<p class="mb-4 text-sm text-base-content/70">
            A meeting is running. <a class="link" href="/admin/">Open meeting</a>
          </p>{/if}
        {#if catalog}
          {#if catalog.length}<div class="mb-4 flex flex-wrap items-center gap-2">
              <label class="input input-sm min-h-11 w-full sm:w-auto sm:flex-1 sm:basis-56 lg:min-h-9"
                ><Search size={15} aria-hidden="true" class="opacity-60" /><input
                  type="search"
                  aria-label="Search templates"
                  placeholder="Search by name or description"
                  autocomplete="off"
                  bind:value={query}
                  oninput={syncURL}
                /></label
              >
              <!-- No legend: daisyUI rounds a join's first and last children. -->
              <div class="join w-full sm:w-auto" role="radiogroup" aria-label="Image status">
                {#each filters as [value, label] (value)}<input
                    class="join-item btn btn-sm min-h-11 flex-1 tabular-nums sm:flex-none lg:min-h-9"
                    type="radio"
                    name="template-status"
                    aria-label={`${label} ${counts[value]}`}
                    {value}
                    bind:group={filter}
                    onchange={syncURL}
                  />{/each}
              </div>
            </div>
            <p class="mb-3 text-xs tabular-nums text-base-content/60" aria-live="polite">
              {filtered
                ? `Showing ${visible.length} of ${plural(cards.length, "template")}`
                : plural(cards.length, "template")}
            </p>{/if}
          <ul class="grid gap-3 sm:grid-cols-2">
            {#each visible as card (card.template.id)}
              {@const t = card.revision}
              <TemplateCard template={card.template} href={adminTemplatePath(card.template.name)}>
                {#snippet badge()}{#if meetingOpen && data?.meeting?.template === card.template.id}<span
                      class="badge badge-sm badge-primary badge-soft relative shrink-0">In meeting</span
                    >{/if}{/snippet}
                {#snippet status()}{#if card.state}<span class="flex flex-col gap-1.5"
                      ><Status status={card.state.status} label={card.state.label} />{#if card.progress}<progress
                          class="progress h-1 w-full max-w-32"
                          value={card.progress.done}
                          max={card.progress.total}
                          aria-hidden="true"
                        ></progress>{/if}</span
                    >{/if}{/snippet}
                {#snippet actions()}
                  {#if t && card.ready && !meetingOpen}<button
                      class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
                      aria-label={`Start meeting: ${templateName(t.name)}`}
                      onclick={() => meetingDialog.show(t)}><Users size={15} />Start meeting</button
                    >{:else if t && buildable(t) && !jobActive(card.job)}<button
                      class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                      aria-label={`${buildLabel(t, card.job)}: ${templateName(t.name)}`}
                      onclick={() => imageDialog.show("build", t)}
                      ><Hammer size={15} />{buildLabel(t, card.job)}</button
                    >{/if}
                {/snippet}
              </TemplateCard>
            {:else}<li class="py-10 text-center sm:col-span-2">
                <BookOpen size={22} class="mx-auto text-base-content/60" aria-hidden="true" />
                {#if catalog.length}<h2 class="mt-3 font-semibold">No matching templates</h2>
                  <button class="btn btn-sm min-h-11 mt-3" onclick={clearFilters}>Clear filters</button>
                {:else}<h2 class="mt-3 font-semibold">No templates yet</h2>
                  <p class="mt-1 text-sm text-base-content/70">Refresh from GitHub to import them.</p>{/if}
              </li>{/each}
          </ul>
          {#if catalogError}<p class="mt-3 text-xs text-warning" role="status">
              Updates are unavailable ({catalogError}).
            </p>{/if}
        {:else if catalogError}<Notice message={catalogError} /><button
            class="btn btn-sm min-h-11"
            onclick={loadCatalog}><RotateCw size={15} />Retry</button
          >
        {:else}<div class="grid gap-3 sm:grid-cols-2" role="status">
            {#each [1, 2] as n (n)}<div class="skeleton h-52 w-full rounded-box"></div>{/each}
            <span class="sr-only">Loading templates…</span>
          </div>{/if}
      </section>
      <!-- On short laptop screens the pinned activity feed scrolls on its own. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <aside
        class="min-w-0 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary lg:sticky lg:top-20 lg:max-h-[calc(100dvh-6rem)] lg:overflow-y-auto lg:overscroll-contain lg:pr-1"
        aria-labelledby="activity-title"
        tabindex="0"
      >
        <Activity {data} onchange={refreshAdmin} />
      </aside>
    </div>
  {:else}<p class="flex items-center gap-2 text-sm text-base-content/60" role="status">
      <span class="loading loading-spinner loading-sm"></span>Loading templates…
    </p>{/if}
</Guard>
<MeetingDialog bind:this={meetingDialog} />
<ImageDialog bind:this={imageDialog} />
