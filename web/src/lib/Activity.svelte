<script lang="ts">
  import {
    Activity,
    Check,
    Clock,
    Monitor,
    RotateCw,
    TriangleAlert,
  } from "@lucide/svelte";
  import type { AdminView, Job } from "$lib/types";
  import { api, adminUpdated, adminPollError } from "$lib/api";
  import { now, relative, duration } from "$lib/time";
  import { jobName } from "$lib/job";
  import Status from "./Status.svelte";
  let { data, onchange }: { data: AdminView; onchange: () => Promise<void> } =
    $props();
  let retrying = $state("");
  let feedback = $state<{ error: boolean; message: string } | null>(null);
  async function retry(job: Job) {
    if (retrying || !job.retryable) return;
    retrying = job.id;
    feedback = null;
    try {
      const next = await api<Job>("/api/admin/jobs/retry", { id: job.id });
      feedback = {
        error: false,
        message: `Recovery ${next.status} for ${target(job)}. Follow its progress under Active.`,
      };
      filter = "active";
      try {
        await onchange();
      } catch {
        feedback.message +=
          " Progress refresh is unavailable; the recovery was accepted.";
      }
    } catch (e) {
      feedback = { error: true, message: (e as Error).message };
      try {
        await onchange();
      } catch {
        /* Keep the action error visible. */
      }
    } finally {
      retrying = "";
    }
  }
  let filter = $state("active");
  const active = $derived(
    data.jobs.filter((j) => ["queued", "running"].includes(j.status)),
  );
  const failed = $derived(
    data.jobs.filter((j) => j.status === "failed" && j.needsAttention),
  );
  let recentLimit = $state(12);
  const matching = $derived(
    data.jobs.filter(
      (j) =>
        (j.kind !== "access-cleanup" || j.status === "failed") &&
        (filter === "all" ||
          (filter === "failed" && j.status === "failed" && j.needsAttention) ||
          (filter === "active" && ["queued", "running"].includes(j.status))),
    ),
  );
  const visible = $derived(
    filter === "all" ? matching.slice(0, recentLimit) : matching,
  );
  const stale = $derived(
    !!$adminPollError || (!!$adminUpdated && $now - $adminUpdated > 10000),
  );
  function target(job: Job) {
    if (job.targetLabel) return job.targetLabel;
    const t = data.templates.find((t) => t.id === job.target);
    if (t) return `${t.name} · ${t.commit.slice(0, 7)}`;
    const r =
      data.exerciseRuns?.find((r) => r.id === job.target) ??
      data.checkRuns?.find((r) => r.id === job.target);
    return (
      data.teams.find((t) => t.id === r?.teamID)?.name ??
      (job.target === data.meeting?.id
        ? data.meeting.name
        : job.kind === "refresh"
          ? "Template repository"
          : job.kind === "access-cleanup"
            ? "Desktop sessions"
            : "Lab operation")
    );
  }
  function waiting(job: Job) {
    return job.waitingOn
      ?.map((id) => {
        const dependency = data.jobs.find((j) => j.id === id);
        return dependency
          ? `${jobName(dependency.kind)} · ${target(dependency)}`
          : "overlapping resource operation";
      })
      .join(", ");
  }
</script>

<section class="scroll-mt-6" aria-labelledby="activity-title">
  <div class="mb-4">
    <h2
      id="activity-title"
      class="flex items-center gap-2 text-base font-semibold"
    >
      <Activity size={17} />Live activity
    </h2>
    <p
      class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs tabular-nums text-base-content/75"
    >
      <span
        >{active.filter((j) => j.status === "running").length} running · {active.filter(
          (j) => j.status === "queued",
        ).length} queued</span
      ><span class="inline-flex items-center gap-1.5"
        ><Monitor size={13} />{data.activeDesktops ?? 0} active desktops</span
      >
    </p>
    <p class="mt-1 text-xs text-base-content/75">
      {$adminUpdated
        ? `Updated ${relative(new Date($adminUpdated).toISOString(), $now)}`
        : "Waiting for a successful update"}
    </p>
    {#if stale}<p class="mt-2 text-sm text-warning" role="status">
        Activity may be stale. Waiting for a successful update.
      </p>{/if}
  </div>
  <fieldset class="flex flex-wrap gap-1 rounded-field bg-base-200 p-1">
    <legend class="sr-only">Activity filter</legend>
    <button
      class="btn btn-sm min-h-11 flex-1 gap-1 px-2 text-xs lg:min-h-8"
      class:btn-primary={filter === "active"}
      class:btn-ghost={filter !== "active"}
      aria-pressed={filter === "active"}
      onclick={() => (filter = "active")}
      >{#if filter === "active"}<Check
          size={13}
          aria-hidden="true"
        />{/if}Active ({active.length})</button
    >
    <button
      class="btn btn-sm min-h-11 flex-1 gap-1 px-2 text-xs lg:min-h-8"
      class:btn-primary={filter === "failed"}
      class:btn-ghost={filter !== "failed"}
      aria-pressed={filter === "failed"}
      onclick={() => (filter = "failed")}
      >{#if filter === "failed"}<Check size={13} aria-hidden="true" />{/if}Needs
      attention
      <span
        class="rounded-selector px-1 tabular-nums"
        class:bg-error={failed.length > 0}
        class:text-error-content={failed.length > 0}>({failed.length})</span
      ></button
    >
    <button
      class="btn btn-sm min-h-11 gap-1 px-2 text-xs lg:min-h-8"
      class:btn-primary={filter === "all"}
      class:btn-ghost={filter !== "all"}
      aria-pressed={filter === "all"}
      onclick={() => (filter = "all")}
      >{#if filter === "all"}<Check
          size={13}
          aria-hidden="true"
        />{/if}Recent</button
    >
  </fieldset>
  {#if failed.length && filter !== "failed"}<div
      class="mt-3 flex flex-wrap items-center gap-2 text-sm"
    >
      <TriangleAlert size={15} class="text-error" aria-hidden="true" />
      <p class="flex-1">
        {failed.length}
        {failed.length === 1 ? "operation needs" : "operations need"} attention.
      </p>
      <button
        class="btn btn-sm min-h-11 btn-ghost text-xs lg:min-h-8"
        onclick={() => (filter = "failed")}>View failures</button
      >
    </div>{/if}
  {#if feedback}<p
      class="mt-3 break-words text-sm"
      class:text-error={feedback.error}
      role={feedback.error ? "alert" : "status"}
    >
      {feedback.message}
    </p>{/if}
  <ul
    class="mt-3 divide-y divide-base-content/10 border-t border-base-content/10"
  >
    {#each visible as job (job.id)}<li class="py-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm font-medium">{jobName(job.kind)}</span><Status
            status={job.status}
          />
        </div>
        <p class="mt-1 break-words text-sm">
          {target(job)}
        </p>
        {#if job.kind === "build"}{@const template = data.templates.find(
            (t) => t.id === job.target,
          )}{#if template}<p
              class="mt-1 text-xs tabular-nums text-base-content/75"
            >
              {template.imagesReady} / {template.imagesTotal} images ready
            </p>{/if}{:else if job.kind === "prepare" && job.target === data.meeting?.id}<p
            class="mt-1 text-xs tabular-nums text-base-content/75"
          >
            {data.teams.filter((t) => t.status === "ready").length} / {data
              .teams.length} team labs ready
          </p>{/if}
        <p class="mt-1 text-xs text-base-content/75">
          {job.started
            ? `Started ${relative(job.started, $now)}`
            : `Queued ${relative(job.created, $now)}`}
          {#if job.started}
            · {duration(
              Date.parse(job.finished || new Date($now).toISOString()) -
                Date.parse(job.started),
            )}{job.status === "running" ? " running" : " duration"}{/if}
        </p>
        {#if job.status === "queued"}<p class="mt-2 break-words text-sm">
            {waiting(job)
              ? `Waiting for ${waiting(job)}`
              : "Waiting for an available worker"}
          </p>{/if}
        {#if job.error}<p
            class={[
              "mt-2 break-words text-sm",
              job.needsAttention ? "text-error" : "text-base-content/75",
            ]}
          >
            {job.error === "context canceled"
              ? "Operation interrupted (context canceled)."
              : job.error}
          </p>{/if}
        {#if job.status === "failed" && job.retryable}<button
            class="btn btn-sm min-h-11 mt-3 lg:min-h-8"
            disabled={!!retrying}
            onclick={() => retry(job)}
            >{#if retrying === job.id}<span
                class="loading loading-spinner loading-xs"
                aria-hidden="true"
              ></span>Queuing recovery…{:else}<RotateCw size={14} />Retry
              recovery{/if}</button
          >{:else if job.retryBlocked}<p class="mt-2 text-sm leading-relaxed">
            {job.retryBlocked}
          </p>{/if}
      </li>{:else}<li class="py-5 text-sm text-base-content/75">
        {filter === "active"
          ? "No operations running or queued."
          : filter === "failed"
            ? "No failed operations."
            : "No operations yet."}
      </li>{/each}
  </ul>
  {#if filter === "all" && matching.length}<div
      class="mt-3 flex flex-wrap items-center gap-2"
    >
      <p class="text-xs text-base-content/75">
        Showing {visible.length} of {matching.length} recent operations in retained
        history.
      </p>
      {#if visible.length < matching.length}<button
          class="btn btn-sm btn-ghost min-h-11"
          onclick={() => (recentLimit += 12)}>Show more</button
        >{/if}
    </div>{/if}
</section>
