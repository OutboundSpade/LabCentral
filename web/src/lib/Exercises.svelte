<script lang="ts">
  import { Play, RotateCcw, Check, Shield, Users } from "@lucide/svelte";
  import { api, date } from "$lib/api";
  import Notice from "$lib/Notice.svelte";
  import Status from "./Status.svelte";
  import type { AdminView } from "$lib/types";
  let { data, onchange }: { data: AdminView; onchange: () => Promise<void> } =
    $props();
  let selected = $state<string[]>([]);
  let action = $state("");
  let scenario = $state("");
  let error = $state("");
  let busy = $state(false);
  let dialog: HTMLDialogElement;
  async function retry(id: string) {
    const job = data.jobs.find(
      (j) => j.kind === "exercise" && j.target === id && j.status === "failed",
    );
    if (!job) return;
    busy = true;
    error = "";
    try {
      await api("/api/admin/jobs/retry", { id: job.id });
      await onchange();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
  const available = $derived(
    data.teams.filter(
      (t) =>
        t.status === "ready" &&
        !data.exerciseRuns?.some(
          (r) =>
            r.teamID === t.id &&
            (["queued", "resetting", "applying", "checking"].includes(
              r.status,
            ) ||
              data.jobs.some(
                (j) =>
                  j.target === r.id && ["queued", "running"].includes(j.status),
              )),
        ),
    ),
  );
  const chosen = $derived(available.filter((t) => selected.includes(t.id)));
  const custom = $derived(
    !!data.templates.find((t) => t.id === data.meeting?.template)?.instructor,
  );
  const definition = $derived(data.exercises?.find((d) => d.id === scenario));
  const open = $derived(
    !!data.meeting &&
      !["ending", "ended"].includes(data.meeting.status) &&
      Date.parse(data.meeting.expires) > Date.now(),
  );
  function confirm(next: string) {
    action = next;
    error = "";
    dialog.showModal();
  }
  async function run(next: string) {
    busy = true;
    error = "";
    try {
      await api("/api/admin/exercises", {
        action: next,
        teamIDs: chosen.map((t) => t.id),
      });
      dialog?.close();
      await onchange();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

{#if data.exercises?.length && open}
  <section aria-labelledby="exercises-title">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2
        id="exercises-title"
        class="flex items-center gap-2 text-base font-semibold"
      >
        <Shield size={17} />Exercises
      </h2>
      <button
        class="btn btn-sm min-h-11"
        disabled={busy || !available.length}
        onclick={() => (selected = available.map((t) => t.id))}
        ><Users size={16} />Select ready teams</button
      >
      <button
        class="btn btn-sm min-h-11 btn-ghost"
        disabled={busy || !selected.length}
        onclick={() => (selected = [])}>Clear selection</button
      >
    </div>
    <p class="mt-2 text-sm leading-relaxed">
      {custom
        ? "Each break runs the pinned template reset first. See instructor notes for the reset scope."
        : "Each break resets the web server first. Bank data and desktop files stay."}
    </p>
    <Notice message={error} dismissible />
    <div class="mt-4">
      <div class="w-full sm:w-72">
        <label class="label mb-2" for="exercise-scenario">Exercise</label
        ><select
          class="select select-sm min-h-11 w-full"
          id="exercise-scenario"
          bind:value={scenario}
          ><option value="" disabled>Choose an exercise</option
          >{#each data.exercises as d}<option value={d.id}>{d.name}</option
            >{/each}</select
        >
      </div>
    </div>
    {#if definition}<details
        class="collapse collapse-arrow mt-4 border border-base-content/10"
      >
        <summary class="collapse-title text-sm font-medium"
          >Instructor walkthrough</summary
        >
        <div class="collapse-content text-sm">
          <p>{definition.description}</p>
          <p class="mt-3 whitespace-pre-line leading-relaxed">
            {definition.walkthrough}
          </p>
        </div>
      </details>{/if}
    <fieldset class="mt-4">
      <legend class="text-sm font-medium">Teams</legend>
      <div class="mt-2 divide-y divide-base-content/10">
        {#each data.teams as team}
          {@const record = data.exerciseRuns?.find((r) => r.teamID === team.id)}
          <div class="py-1">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <label class="flex min-h-11 items-center gap-3"
                ><input
                  class="checkbox checkbox-sm"
                  type="checkbox"
                  value={team.id}
                  aria-label={team.name}
                  bind:group={selected}
                  disabled={busy || !available.some((t) => t.id === team.id)}
                /><span class="text-sm font-medium">{team.name}</span></label
              ><Status
                status={record?.status ?? team.status}
                label={!record && team.status === "ready"
                  ? "No exercise"
                  : undefined}
              />
            </div>
            {#if record}<div class="mt-1 mb-3 ml-7 text-xs">
                <p>
                  {data.exercises.find((d) => d.id === record.action)?.name ??
                    (record.action === "reset"
                      ? "Reset web server"
                      : "Check recovery")} · {date(record.started)}
                </p>
                <p
                  class="mt-1 break-words text-sm"
                  class:text-error={record.status === "failed"}
                >
                  {record.report || "In progress"}
                </p>
                {#if record.status === "failed" && data.jobs.some((j) => j.target === record.id && j.status === "failed")}<button
                    class="btn btn-sm min-h-11 mt-2"
                    disabled={busy || !available.some((t) => t.id === team.id)}
                    onclick={() => retry(record.id)}
                    ><RotateCcw size={14} />Retry recovery</button
                  >{/if}
              </div>{/if}
            {#if !available.some((t) => t.id === team.id)}<p
                class="mb-3 ml-7 text-sm"
              >
                {team.status !== "ready"
                  ? "Lab is " +
                    team.status +
                    ". It must be ready before an exercise can run."
                  : "An exercise is already in progress for this team."}
              </p>{/if}
          </div>{/each}
      </div>
    </fieldset>
    <p class="mt-4 text-sm">
      {chosen.length
        ? `${chosen.length} ${chosen.length === 1 ? "team" : "teams"} selected. ${scenario ? "Ready to run the selected exercise." : "Choose an exercise to run a break, or reset the selected teams."}`
        : "Select ready teams, then choose an exercise."}
    </p>
    <div
      class="sticky bottom-24 z-10 mt-4 border-t border-base-content/10 bg-base-100 py-4 lg:bottom-4"
    >
      <div class="flex flex-wrap items-center gap-2">
        <button
          class="btn btn-sm min-h-11 btn-primary"
          disabled={busy || !scenario || !chosen.length}
          onclick={() => confirm(scenario)}
          ><Play size={16} />Run on {chosen.length}
          {chosen.length === 1 ? "team" : "teams"}</button
        ><button
          class="btn btn-sm min-h-11"
          disabled={busy || !chosen.length}
          onclick={() => confirm("reset")}
          ><RotateCcw size={16} />{custom
            ? "Reset exercise baseline"
            : "Reset web server"}</button
        >{#if !custom}<button
            class="btn btn-sm min-h-11 btn-ghost"
            disabled={busy || !chosen.length}
            onclick={() => run("check")}
            ><Check size={16} />Check recovery</button
          >{/if}
      </div>
    </div>
  </section>
{/if}
<dialog
  class="modal"
  bind:this={dialog}
  aria-labelledby="exercise-confirm-title"
>
  <div class="modal-box">
    <h2 class="text-base font-semibold" id="exercise-confirm-title">
      {action === "reset"
        ? "Reset the exercise baseline?"
        : "Run this exercise?"}
    </h2>
    <p class="mt-4 font-medium">
      {chosen.length}
      {chosen.length === 1 ? "team" : "teams"} selected
    </p>
    <details class="collapse collapse-arrow mt-3 border border-base-content/10">
      <summary class="collapse-title text-sm">Selected teams</summary>
      <div class="collapse-content">
        <!-- Keyboard scrolling for large team selections. -->
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <ul
          class="max-h-48 overflow-y-auto text-sm"
          tabindex="0"
          aria-label="Selected teams"
        >
          {#each chosen as team}<li class="py-1">{team.name}</li>{/each}
        </ul>
      </div>
    </details>
    <p class="mt-3 text-sm leading-relaxed">
      {#if custom}This runs the pinned template reset for the selected teams.
        Review its scope in the instructor notes. {action === "reset"
          ? ""
          : "The selected break will then be applied."}{:else}This restores
        Apache configuration, the homepage, and the exercise support account,
        replacing changes to those items. {action === "reset"
          ? "The web server will return to its working baseline."
          : "The selected break will then be applied."} Bank data, published documents,
        desktop files, and historical logs are preserved.{/if}
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={busy}>Cancel</button>
      </form>
      <button
        class="btn btn-sm min-h-11 btn-primary"
        disabled={busy || !chosen.length || !open}
        onclick={() => run(action)}
        >{busy
          ? "Queuing…"
          : action === "reset"
            ? "Reset selected teams"
            : "Run selected exercise"}</button
      >
    </div>
  </div>
</dialog>
