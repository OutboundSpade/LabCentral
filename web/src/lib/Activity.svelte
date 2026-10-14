<script lang="ts">
  import { RotateCw } from "@lucide/svelte";
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
      data.breakRuns?.find((r) => r.id === job.target) ??
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

<section class="scroll-mt-24" aria-labelledby="activity-title">
  <div class="mb-3 flex items-baseline justify-between gap-3">
    <h2 id="activity-title" class="text-sm font-semibold">Live activity</h2>
    <p class="text-xs text-base-content/60">
      {$adminUpdated
        ? `Updated ${relative(new Date($adminUpdated).toISOString(), $now)}`
        : "Waiting for an update"}
    </p>
  </div>
  {#if stale}<p class="mb-3 text-sm text-warning" role="status">
      Activity may be stale. Waiting for a successful update.
    </p>{/if}
  <fieldset class="flex gap-1 rounded-field bg-base-200 p-1">
    <legend class="sr-only">Activity filter</legend>
    {#each [["active", "Active", active.length], ["failed", "Needs attention", failed.length], ["all", "Recent", -1]] as [value, label, count]}<button
        class={[
          "flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-[calc(var(--radius-field)-2px)] px-2 text-xs whitespace-nowrap lg:min-h-8",
          filter === value
            ? "bg-base-300 font-medium text-base-content"
            : "text-base-content/70 hover:text-base-content",
        ]}
        aria-pressed={filter === value}
        onclick={() => (filter = value as string)}
        >{label}{#if (count as number) >= 0}{" "}<span
            class={[
              "rounded-selector px-1 tabular-nums",
              value === "failed" && (count as number) > 0
                ? "bg-error text-error-content"
                : "",
            ]}>({count})</span
          >{/if}</button
      >{/each}
  </fieldset>
  {#if feedback}<p
      class="mt-3 break-words text-sm"
      class:text-error={feedback.error}
      role={feedback.error ? "alert" : "status"}
    >
      {feedback.message}
    </p>{/if}
  <ul class="mt-2 divide-y divide-base-content/8">
    {#each visible as job (job.id)}<li class="py-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm font-medium">{jobName(job.kind)}</span><Status
            status={job.status}
          />
        </div>
        <p class="mt-0.5 break-words text-sm text-base-content/70">
          {target(job)}
        </p>
        {#if job.kind === "build"}{@const template = data.templates.find(
            (t) => t.id === job.target,
          )}{#if template}<p
              class="mt-1 text-xs tabular-nums text-base-content/60"
            >
              {template.imagesReady} / {template.imagesTotal} images ready
            </p>{/if}{:else if job.kind === "prepare" && job.target === data.meeting?.id}<p
            class="mt-1 text-xs tabular-nums text-base-content/60"
          >
            {data.teams.filter((t) => t.status === "ready").length} / {data
              .teams.length} team labs ready
          </p>{/if}
        <p class="mt-1 text-xs text-base-content/60">
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
              job.needsAttention ? "text-error" : "text-base-content/60",
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
      </li>{:else}<li class="py-6 text-center text-sm text-base-content/60">
        {filter === "active"
          ? "Nothing running."
          : filter === "failed"
            ? "No failures."
            : "No operations yet."}
      </li>{/each}
  </ul>
  {#if filter === "all" && matching.length}<div
      class="mt-3 flex flex-wrap items-center gap-2"
    >
      <p class="text-xs text-base-content/60">
        Showing {visible.length} of {matching.length}
      </p>
      {#if visible.length < matching.length}<button
          class="btn btn-sm btn-ghost min-h-11"
          onclick={() => (recentLimit += 12)}>Show more</button
        >{/if}
    </div>{/if}
</section>
