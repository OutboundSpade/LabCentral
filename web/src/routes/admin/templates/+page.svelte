<script lang="ts">
  import { templateName } from "$lib/display";
  import Activity from "$lib/Activity.svelte";
  import { onMount } from "svelte";
  import {
    GitCommitHorizontal,
    RefreshCw,
    Hammer,
    History,
    Check,
    Trash2,
    Clock,
    LockKeyhole,
    LibraryBig,
    TriangleAlert,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import AdminNav from "$lib/AdminNav.svelte";
  import Status from "$lib/Status.svelte";
  import PollStatus from "$lib/PollStatus.svelte";
  import { jobName } from "$lib/job";
  import Notice from "$lib/Notice.svelte";
  import { api, session, adminView, refreshAdmin, date } from "$lib/api";
  import type { Template } from "$lib/types";
  let data = $derived($adminView);
  let error = $state("");
  let busy = $state(false);
  let selected = $state<Template | null>(null);
  let action = $state<"build" | "remove-images" | "prune-images">("build");
  let confirmation: HTMLDialogElement;
  const refreshing = $derived(
    data?.jobs.some(
      (j) =>
        j.kind === "refresh" &&
        (j.status === "queued" || j.status === "running"),
    ) ?? false,
  );
  function jobFor(id: string) {
    return (
      data?.templates.find((t) => t.id === id)?.operation ??
      data?.jobs.find(
        (j) =>
          j.target === id && (j.kind === "build" || j.kind === "remove-images"),
      )
    );
  }
  function active(id: string) {
    const j = jobFor(id);
    return j?.status === "queued" || j?.status === "running";
  }
  async function load() {
    await refreshAdmin().catch(() => {});
  }
  onMount(() => {
    void load();
  });
  async function run(path: string, body: unknown = {}) {
    busy = true;
    error = "";
    try {
      await api(path, body);
      confirmation?.close();
      await load();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
  function confirm(template: Template, operation: "build" | "remove-images") {
    selected = template;
    action = operation;
    error = "";
    confirmation.showModal();
  }
  const pruning = $derived(data?.jobs.some((j) => j.kind === "prune-images" && ["queued", "running"].includes(j.status)) ?? false);
  const pruneAttention = $derived(data?.jobs.some((j) => j.kind === "prune-images" && j.needsAttention) ?? false);
  let pruneCount = $state(0);
  let history = $state(false);
  function featured(template: Template, index: number) {
    return (
      index < 3 ||
      template.latest ||
      data?.meeting?.template === template.id ||
      active(template.id)
    );
  }
  const earlier = $derived(
    data?.templates.filter((t, index) => !featured(t, index)).length ?? 0,
  );
</script>

<svelte:head><title>Templates · LabCentral</title></svelte:head>
<PageHeader title="Templates">
  {#if $session?.user.admin}<a class="btn btn-sm min-h-11 lg:min-h-8" href="/labs/"
      ><LibraryBig size={15} />Student library</a
    ><button
      class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
      disabled={busy || refreshing}
      onclick={() => run("/api/admin/templates/refresh")}
      >{#if refreshing}<span class="loading loading-spinner loading-xs"
        ></span>{:else}<RefreshCw size={15} />{/if}{refreshing
        ? "Refreshing…"
        : "Refresh from GitHub"}</button
    >{/if}
</PageHeader>
{#if $session?.user.admin}<AdminNav />{/if}
<Guard admin
  ><Notice message={error} dismissible /><PollStatus />
  {#if data}
    <div class="mb-6 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm" aria-live="polite">
      {#if data.imageSummary}<p>{data.imageSummary.images} images · {data.imageSummary.unusedImages} unused</p>
        <button class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8" disabled={busy || pruning || pruneAttention || !data.imageSummary.unusedImages} title={pruneAttention ? "Retry cleanup in Live activity" : undefined}
          onclick={() => { selected = null; action = "prune-images"; pruneCount = data?.imageSummary?.unusedImages ?? 0; error = ""; confirmation.showModal(); }}>
          <Trash2 size={15} />{pruning ? "Removing unused images…" : "Remove unused images"}
        </button>
      {:else}<p class="text-base-content/70">Image inventory unavailable.</p>{/if}
    </div>
    {#if data.jobs.find((j) => j.kind === "refresh")?.status === "failed"}<Notice
        message={data.jobs.find((j) => j.kind === "refresh")?.error ??
          "GitHub refresh failed. Please retry."}
      />{/if}
    {#if data.importFailures?.length}<div
        role="alert"
        class="alert alert-soft alert-warning my-5 items-start"
      >
        <TriangleAlert size={18} aria-hidden="true" class="mt-0.5 shrink-0" />
        <div class="min-w-0">
          <p class="font-medium">
            {data.importFailures.length === 1
              ? "1 template was not imported"
              : `${data.importFailures.length} templates were not imported`}
          </p>
          <p class="text-sm text-base-content/70">
            Other templates imported normally. Fix these in the repository, then refresh.
          </p>
          <ul class="mt-2 space-y-1.5 text-sm">
            {#each data.importFailures as failure}<li class="break-words">
                <span class="font-medium">{failure.directory}</span>
                <code class="ml-1 text-xs text-base-content/80">{failure.error}</code>
              </li>{/each}
          </ul>
        </div>
      </div>{/if}
    <div
      class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-12"
    >
      <section class="min-w-0">
        <ul
          class="rounded-box border border-base-content/9 bg-base-200/70 divide-y divide-base-content/8"
          aria-label="Template revision history"
        >
          {#each data.templates as template, index (template.id)}
            {@const job = jobFor(template.id)}{@const working = active(
              template.id,
            )}
            <li class="px-5 py-5" hidden={!history && !featured(template, index)}>
              <article
                aria-label={`${template.name} revision ${template.commit.slice(0, 7)}`}
              >
                <div class="mb-2 flex flex-wrap items-center gap-3 text-xs">
                  <span class="font-mono text-base-content/70">{template.name}</span
                  >{#if template.latest}<span class="badge badge-sm badge-ghost">Latest</span
                    >{/if}{#if data.meeting?.template === template.id && data.meeting.status !== "ended"}<span
                      class="inline-flex items-center gap-1 text-base-content/60"
                      ><LockKeyhole size={12} />In use</span
                    >{/if}
                </div>
                <h2 class="text-base font-semibold leading-snug">
                  {templateName(template.name)}
                </h2>
                <p class="mt-1 text-sm text-base-content/70">{template.commitTitle || "Imported revision"}</p>
                <div
                  class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-base-content/60"
                >
                  {#if data.repositoryURL}<a
                      class="link link-hover inline-flex items-center gap-1.5"
                      href={`${data.repositoryURL}/commit/${template.commit}`}
                      target="_blank"
                      rel="noreferrer"
                      ><GitCommitHorizontal size={14} /><code
                        >{template.commit.slice(0, 7)}</code
                      ></a
                    >{:else}<code>{template.commit.slice(0, 7)}</code>{/if}
                  <span class="inline-flex items-center gap-1.5"
                    ><Clock size={13} />{#if template.committedAt}<time
                        datetime={template.committedAt}
                        >{date(template.committedAt, {
                          dateStyle: "medium",
                          timeStyle: "short",
                        })}</time
                      >{:else}Date unavailable — refresh from GitHub{/if}</span
                  >
                </div>
                <div
                  class="mt-3 flex flex-wrap items-center gap-3 text-xs"
                  aria-live="polite"
                >
                  <Status
                    status={working
                      ? (job?.status ?? "queued")
                      : job?.status === "failed"
                        ? "failed"
                        : template.removing
                          ? "queued"
                          : template.removedAt
                            ? "ended"
                            : template.built
                              ? "ready"
                              : "pending"}
                    label={working
                      ? job?.status === "queued"
                        ? "Queued"
                        : job?.kind === "build"
                          ? "Building images"
                          : "Removing images"
                      : job?.status === "failed"
                        ? job.kind === "build"
                          ? "Build failed"
                          : "Cleanup failed"
                        : template.removing
                          ? "Cleanup pending"
                          : template.removedAt
                            ? "Images removed"
                            : template.built
                              ? "Ready to use"
                              : "Not built"}
                  />
                  {#if !template.removedAt}<span
                      class="tabular-nums text-base-content/60"
                      >{#if !template.built && template.imagesReady === template.imagesTotal && template.imagesTotal > 0}Images
                        ready — confirm build to use{:else}{template.imagesReady}
                        / {template.imagesTotal} images ready{/if}</span
                    >{/if}
                </div>
                {#if job?.waitingOn?.length}<p
                    class="mt-2 text-sm leading-relaxed"
                  >
                    Waiting for {job.waitingOn
                      .map((id) =>
                        jobName(
                          data?.jobs.find((j) => j.id === id)?.kind ?? "",
                        ),
                      )
                      .join(", ")} on shared resources.
                  </p>{/if}
                {#if template.instructor}<details
                    class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mt-4"
                  >
                    <summary
                      class="collapse-title flex items-center gap-2 text-sm font-medium"
                      ><LockKeyhole size={14} />Instructor only · {template.commit.slice(
                        0,
                        7,
                      )}</summary
                    >
                    <div class="collapse-content text-sm">
                      <p class="whitespace-pre-line leading-relaxed">
                        {template.instructor.notes}
                      </p>
                      <ul class="mt-4 space-y-3">
                        {#each template.instructor.breaks as exercise}<li>
                            <p class="font-medium">{exercise.name}</p>
                            <p
                              class="mt-1 whitespace-pre-line text-base-content/60"
                            >
                              {exercise.walkthrough}
                            </p>
                          </li>{/each}
                      </ul>
                      <p class="mt-4 text-xs text-base-content/60">
                        {template.instructor.checks.length} scheduled service checks
                        available.
                      </p>
                    </div>
                  </details>{/if}
                {#if template.removedAt}<p
                    class="mt-2 text-xs text-base-content/60"
                  >
                    Removed {date(template.removedAt)}. Images shared by other
                    revisions are retained.
                  </p>{:else if template.builtAt}<p
                    class="mt-2 text-xs text-base-content/60"
                  >
                    Built {date(template.builtAt)}
                  </p>{/if}
                {#if job?.status === "failed"}<p
                    class="mt-3 break-words text-sm text-error"
                  >
                    {job.error}
                  </p>{/if}
                <div class="mt-3 flex flex-wrap gap-2">
                  <a
                    class="btn btn-sm min-h-11 lg:min-h-8"
                    href={`/labs/?template=${encodeURIComponent(template.id)}`}
                    aria-label={`Open ${template.name} revision ${template.commit.slice(0, 7)} in the library`}
                    ><LibraryBig size={15} />Open in library</a
                  >
                  {#if !template.built && !template.removing}<button
                      class="btn btn-sm min-h-11 lg:min-h-8"
                      class:btn-primary={template.latest}
                      disabled={busy || working}
                      onclick={() => confirm(template, "build")}
                      ><Hammer size={15} />{job?.kind === "build" &&
                      job.status === "failed"
                        ? "Retry build"
                        : template.removedAt
                          ? "Rebuild images"
                          : "Build images"}</button
                    >{/if}
                  {#if template.imagesReady > 0 || template.buildsPending > 0 || template.removing || (job?.kind === "build" && job.status === "failed")}<button
                      class="btn btn-sm min-h-11 btn-error btn-soft lg:min-h-8"
                      disabled={busy || working || !!template.removalBlocked}
                      onclick={() => confirm(template, "remove-images")}
                      ><Trash2 size={15} />{template.removing
                        ? "Retry cleanup"
                        : "Remove images"}</button
                    >{/if}
                </div>
                {#if template.removalBlocked}<p
                    class="mt-2 flex items-start gap-2 text-sm leading-relaxed"
                  >
                    <LockKeyhole
                      size={13}
                      class="mt-0.5 shrink-0"
                    />{template.removalBlocked}
                  </p>{/if}
              </article>
            </li>
          {:else}<li class="px-5 py-10 text-center">
              <History size={22} class="mx-auto text-base-content/60" />
              <h2 class="mt-3 text-base font-semibold">No revisions yet</h2>
              <p class="mt-2 text-sm text-base-content/60">
                Refresh from GitHub to import your templates.
              </p>
            </li>{/each}
        </ul>
        {#if earlier}<button
            type="button"
            class="btn btn-sm min-h-11 mt-3"
            aria-expanded={history}
            onclick={() => (history = !history)}
            ><History size={15} />{history
              ? "Show fewer revisions"
              : `Show ${earlier} earlier ${earlier === 1 ? "revision" : "revisions"}`}</button
          >{/if}
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
  {:else}<p
      class="flex items-center gap-2 text-sm text-base-content/60"
      role="status"
    >
      <span class="loading loading-spinner loading-sm"></span>Loading revisions…
    </p>{/if}
</Guard>
<dialog
  bind:this={confirmation}
  class="modal"
  aria-labelledby="revision-action-title"
>
  <div class="modal-box">
    <h2 id="revision-action-title" class="text-lg font-semibold">
      {action === "prune-images" ? "Remove unused images?" : action === "build" ? "Build images?" : "Remove images?"}
    </h2>
    {#if selected}<p class="mt-3 text-sm font-medium">
      {templateName(selected.name)} · <code>{selected.commit.slice(0, 7)}</code>
    </p>{/if}
    <p class="mt-3 text-sm leading-relaxed">
      {action === "prune-images"
        ? `Currently ${pruneCount} unused images. Latest revisions, open meetings, active and pending practice, and builds are kept. Old revisions stay in history and can be rebuilt.`
        : action === "build"
        ? `Builds ${selected?.imagesTotal ?? 0} images from this revision. Temporary build machines incur charges; matching images are reused.`
        : "Removes this revision’s unused images and unfinished builds. Shared images are kept, and the revision can be rebuilt."}
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11">Cancel</button>
      </form>
      <button
        class="btn btn-sm min-h-11"
        class:btn-primary={action === "build"}
        class:btn-error={action !== "build"}
        disabled={busy}
        onclick={() =>
          run(action === "prune-images" ? "/api/admin/images/prune" : "/api/admin/templates/" + action, action === "prune-images" ? {} : { id: selected?.id })}
        >{#if busy}<span class="loading loading-spinner loading-xs"
          ></span>{/if}{action === "build"
          ? "Start build"
          : "Remove unused images"}</button
      >
    </div>
  </div>
</dialog>
