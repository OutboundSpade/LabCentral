<script lang="ts">
  import { templateName } from "$lib/display";
  import Activity from "$lib/Activity.svelte";
  import { onMount } from "svelte";
  import {
    BookOpen,
    Hammer,
    RefreshCw,
    RotateCw,
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
  import { templatePath, type LibraryTemplate } from "$lib/library";
  import { api, session, adminView, refreshAdmin } from "$lib/api";
  let data = $derived($adminView);
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
  // One card per template, showing its newest revision (the catalog is newest first).
  const cards = $derived(
    [
      ...new Map(
        (catalog ?? []).toReversed().map((t) => [t.name, t]),
      ).values(),
    ].toSorted((a, b) => a.name.localeCompare(b.name)),
  );
  async function loadCatalog() {
    try {
      catalog = (await api<{ templates: LibraryTemplate[] }>("/api/templates")).templates;
      catalogError = "";
    } catch (e) {
      catalogError = (e as Error).message;
    }
  }
  // Reload card content when revisions are imported or removed.
  const revisions = $derived(data?.templates.map((t) => t.id).join(",") ?? null);
  $effect(() => {
    // An empty installation still needs a catalog response to render its empty state.
    if (revisions !== null) void loadCatalog();
  });
  onMount(() => {
    void refreshAdmin().catch(() => {});
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
  {#if data}
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
        <div
          class="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-2 text-sm"
          aria-live="polite"
        >
          {#if data.imageSummary}<p class="tabular-nums text-base-content/70">
              {data.imageSummary.images} images · {data.imageSummary.unusedImages} unused
            </p>
            {#if data.imageSummary.unusedImages || pruning || pruneAttention}<button
                class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8"
                disabled={pruning || pruneAttention}
                title={pruneAttention ? "Retry cleanup in Live activity" : undefined}
                onclick={() =>
                  imageDialog.show("prune-images", null, data?.imageSummary?.unusedImages ?? 0)}
                ><Trash2 size={15} />{pruning
                  ? "Removing unused images…"
                  : "Remove unused images"}</button
              >{/if}
          {:else}<p class="text-base-content/70">Image inventory unavailable.</p>{/if}
        </div>
        {#if meetingOpen}<p class="mb-4 text-sm text-base-content/70">
            A meeting is running. <a class="link" href="/admin/">Open meeting</a>
          </p>{/if}
        {#if catalog}
          <ul class="grid gap-3 sm:grid-cols-2">
            {#each cards as card (card.id)}
              {@const t = data.templates.find((x) => x.id === card.id)}
              {@const job = revisionJob(data, card.id)}
              {@const current = t ? revisionState(t, job) : null}
              <TemplateCard template={card} href={templatePath(card.id)}>
                {#snippet badge()}{#if meetingOpen && data?.meeting?.template === card.id}<span
                      class="badge badge-sm badge-primary badge-soft shrink-0">In meeting</span
                    >{/if}{/snippet}
                {#snippet status()}{#if current}<Status
                      status={current.status}
                      label={current.label}
                    />{/if}{/snippet}
                {#snippet actions()}
                  <a
                    class="btn btn-sm min-h-11 lg:min-h-8"
                    href={templatePath(card.id)}
                    aria-label={`Explore ${templateName(card.name)} revision ${card.commit.slice(0, 7)}`}
                    >Explore</a
                  >
                  {#if t?.built && !t.removing && !t.retired && !meetingOpen}<button
                      class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
                      aria-label={`Start meeting: ${templateName(card.name)}`}
                      onclick={() => meetingDialog.show(t)}><Users size={15} />Start meeting</button
                    >{:else if t && buildable(t)}<button
                      class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
                      disabled={jobActive(job)}
                      onclick={() => imageDialog.show("build", t)}
                      ><Hammer size={15} />{buildLabel(t, job)}</button
                    >{/if}
                {/snippet}
              </TemplateCard>
            {:else}<li class="py-10 text-center sm:col-span-2">
                <BookOpen size={22} class="mx-auto text-base-content/60" aria-hidden="true" />
                <h2 class="mt-3 font-semibold">No templates yet</h2>
                <p class="mt-1 text-sm text-base-content/70">Refresh from GitHub to import them.</p>
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
