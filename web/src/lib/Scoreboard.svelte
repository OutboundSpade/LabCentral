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
  let scheduleScope = $state("");
  let savedInterval = $state(0);
  let dirty = $state(false);
  $effect(() => {
    const scope = data.meeting?.id ?? "";
    const saved = data.checkSchedule?.intervalSeconds ?? 0;
    if (scope !== scheduleScope) {
      scheduleScope = scope;
      interval = saved || 120;
      dirty = false;
    } else if (saved !== savedInterval && !dirty) interval = saved || 120;
    savedInterval = saved;
  });
  const intervals = $derived(
    [...new Set([60, 120, 300, 600, 900, 1800, 3600, interval])].sort(
      (a, b) => a - b,
    ),
  );
  function every(seconds: number) {
    if (seconds % 60) return `Every ${seconds} seconds`;
    const minutes = seconds / 60;
    return `Every ${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
  }
  const template = $derived(
    data.templates.find((t) => t.id === data.meeting?.template),
  );
  const checks = $derived(template?.instructor?.checks ?? []);
  const open = $derived(
    !!data.meeting && !["ending", "ended"].includes(data.meeting.status),
  );
  const total = $derived(checks.reduce((sum, c) => sum + c.points, 0));
  const runs = $derived(
    new Map(data.checkRuns?.map((r) => [r.teamID, r]) ?? []),
  );
  async function run(path: string, body: unknown = {}) {
    busy = true;
    error = "";
    try {
      await api(path, body);
      if (path.endsWith("/schedule")) dirty = false;
      await onchange();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

{#if checks.length && open}
  <section class="min-w-0" aria-labelledby="scoreboard-title">
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
    <Notice message={error} dismissible />
    <form
      class="mt-4 flex flex-wrap items-end gap-2 border-b border-base-content/10 pb-4"
      onsubmit={(e) => {
        e.preventDefault();
        void run("/api/admin/checks/schedule", { intervalSeconds: interval });
      }}
    >
      <div>
        <label class="label mb-2" for="check-interval">Schedule interval</label>
        <select
          id="check-interval"
          class="select select-sm min-h-11"
          bind:value={interval}
          onchange={() => (dirty = true)}
        >
          {#each intervals as seconds}<option value={seconds}
              >{every(seconds)}</option
            >{/each}
        </select>
      </div>
      <button class="btn btn-sm min-h-11" disabled={busy}
        >{data.checkSchedule?.intervalSeconds
          ? "Update schedule"
          : "Start scheduled checks"}</button
      >
      {#if data.checkSchedule?.intervalSeconds}<button
          type="button"
          class="btn btn-sm min-h-11 btn-ghost"
          disabled={busy}
          onclick={() =>
            run("/api/admin/checks/schedule", { intervalSeconds: 0 })}
          >Pause checks</button
        >{/if}
    </form>
    <p class="mt-3 flex items-center gap-2 text-sm">
      <Clock size={14} />{data.checkSchedule?.intervalSeconds
        ? `${every(data.checkSchedule.intervalSeconds)} · next ${date(data.checkSchedule.next)}`
        : "Scheduled checks paused. Check now runs once on ready teams."}
    </p>
    <!-- Keyboard focus lets users scroll a table wider than the viewport. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
      class="mt-4 max-h-[36rem] max-w-full overflow-x-auto overflow-y-auto rounded-box border border-base-content/10"
      tabindex="0"
      role="region"
      aria-label="Service checks table"
    >
      <table class="table table-sm table-pin-rows">
        <caption class="sr-only"
          >Current service health by team. Points are a snapshot, not
          accumulated scores.</caption
        >
        <thead
          ><tr
            ><th scope="col">Team</th>{#each checks as check}<th scope="col"
                >{check.name}<span class="block text-xs font-normal"
                  >{check.points} points</span
                ></th
              >{/each}<th scope="col">Total</th><th scope="col">Last run</th
            ></tr
          ></thead
        >
        <tbody>
          {#each data.teams as team (team.id)}
            {@const record = runs.get(team.id)}
            {@const results = new Map(
              record?.results?.map((r) => [r.id, r]) ?? [],
            )}
            {@const points =
              record?.results?.reduce(
                (sum, c) => sum + (c.passed ? c.points : 0),
                0,
              ) ?? 0}
            <tr aria-label={`${team.name} service checks`}>
              <th scope="row" class="whitespace-nowrap">{team.name}</th>
              {#each checks as check}
                {@const result = results.get(check.id)}
                <td class="min-w-36">
                  <Status
                    soft
                    status={result
                      ? result.passed
                        ? "passed"
                        : result.status
                      : "pending"}
                  />
                  {#if result && !result.passed && result.report}<details
                      class="mt-2 text-sm"
                    >
                      <summary class="cursor-pointer"
                        >Details<span class="sr-only"
                          >: {team.name}, {check.name}</span
                        ></summary
                      >
                      <p class="mt-2 max-w-60 whitespace-normal break-words">
                        {result.report}
                      </p>
                    </details>{/if}
                </td>
              {/each}
              <td class="whitespace-nowrap tabular-nums"
                >{record?.results?.length ? points : "—"} / {total}</td
              >
              <td class="whitespace-nowrap text-xs text-base-content/75"
                >{#if record}<time datetime={record.updated}
                    >{date(record.updated)}</time
                  ><span class="mt-1 block">{record.status}</span>{:else}Not
                  checked{/if}</td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/if}
