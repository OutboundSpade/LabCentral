<script lang="ts">
  import { ListChecks, Play, Clock, LockKeyhole } from "@lucide/svelte";
  import { api, date } from "$lib/api";
  import Notice from "$lib/Notice.svelte";
  import Status from "./Status.svelte";
  import type { AdminView } from "$lib/types";
  let { data, onchange }: { data: AdminView; onchange: () => Promise<void> } =
    $props();
  let error = $state("");
  let busy = $state(false);
  let interval = $state(120);
  const template = $derived(
    data.templates.find((t) => t.id === data.meeting?.template),
  );
  const checks = $derived(template?.instructor?.checks ?? []);
  const open = $derived(
    !!data.meeting && !["ending", "ended"].includes(data.meeting.status),
  );
  const total = $derived(checks.reduce((sum, c) => sum + c.points, 0));
  async function run(path: string, body: unknown = {}) {
    busy = true;
    error = "";
    try {
      await api(path, body);
      await onchange();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

{#if checks.length && open}
  <section aria-labelledby="scoreboard-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h2
          id="scoreboard-title"
          class="flex items-center gap-2 text-base font-semibold"
        >
          <ListChecks size={17} />Service scoreboard
        </h2>
        <p class="mt-2 flex items-center gap-2 text-xs text-base-content/75">
          <LockKeyhole size={14} />Instructor only · checks from revision {template?.commit.slice(
            0,
            7,
          )}
        </p>
      </div>
      <button
        class="btn btn-sm min-h-11"
        disabled={busy || !data.teams.some((t) => t.status === "ready")}
        onclick={() => run("/api/admin/checks/run")}
        ><Play size={16} />Check now</button
      >
    </div>
    <Notice message={error} />
    <form
      class="mt-4 flex flex-wrap items-end gap-2 border-b border-base-content/10 pb-4"
      onsubmit={(e) => {
        e.preventDefault();
        void run("/api/admin/checks/schedule", { intervalSeconds: interval });
      }}
    >
      <div>
        <label class="label mb-2" for="check-interval">Schedule interval</label
        ><select
          id="check-interval"
          class="select select-sm min-h-11"
          bind:value={interval}
          ><option value={60}>Every minute</option><option value={120}
            >Every 2 minutes</option
          ><option value={300}>Every 5 minutes</option><option value={600}
            >Every 10 minutes</option
          ></select
        >
      </div>
      <button class="btn btn-sm min-h-11" disabled={busy}
        >{data.checkSchedule?.intervalSeconds
          ? "Update schedule"
          : "Start scheduled checks"}</button
      >{#if data.checkSchedule?.intervalSeconds}<button
          type="button"
          class="btn btn-sm min-h-11 btn-ghost"
          disabled={busy}
          onclick={() =>
            run("/api/admin/checks/schedule", { intervalSeconds: 0 })}
          >Pause checks</button
        >{/if}
    </form>
    <p class="mt-3 flex items-center gap-2 text-sm text-base-content/75">
      <Clock size={14} />{data.checkSchedule?.intervalSeconds
        ? `Every ${data.checkSchedule.intervalSeconds / 60} minutes · next ${date(data.checkSchedule.next)}`
        : "Scheduled checks paused. Check now runs once on ready teams."}
    </p>
    <div class="mt-4 divide-y divide-base-content/10">
      {#each data.teams as team}
        {@const run = data.checkRuns?.find((r) => r.teamID === team.id)}
        {@const points =
          run?.results?.reduce(
            (sum, c) => sum + (c.passed ? c.points : 0),
            0,
          ) ?? 0}
        <article
          class="min-w-0 py-4"
          aria-label={`${team.name} service checks`}
        >
          <div class="flex items-center justify-between gap-3">
            <h3 class="text-sm font-medium">{team.name}</h3>
            <span class="text-sm tabular-nums text-base-content/75"
              >{run?.results?.length ? points : "—"} / {total}</span
            >
          </div>
          <p class="mt-2 text-xs text-base-content/75">
            {run ? `${run.status} · ${date(run.updated)}` : "Not checked"}
          </p>
          <ul class="mt-3 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            {#each checks as check}{@const result = run?.results?.find(
                (c) => c.id === check.id,
              )}
              <li>
                <div class="flex items-start justify-between gap-3">
                  <span>{check.name}</span><Status
                    status={result
                      ? result.passed
                        ? "passed"
                        : result.status
                      : "pending"}
                  />
                </div>
                {#if result && !result.passed}<p
                    class="mt-1 break-words text-xs text-base-content/75"
                  >
                    {result.report}
                  </p>{/if}
              </li>{/each}
          </ul>
        </article>
      {/each}
    </div>
  </section>
{/if}
