<script lang="ts">
  import { Play, RotateCcw, Check, Users, LockKeyhole, BookOpen } from "@lucide/svelte";
  import { api, date } from "$lib/api";
  import { render, prose } from "$lib/markdown";
  import Notice from "$lib/Notice.svelte";
  import Status from "./Status.svelte";
  import type { AdminView } from "$lib/types";
  let { data, onchange }: { data: AdminView; onchange: () => Promise<void> } =
    $props();
  let selected = $state<string[]>([]);
  let action = $state("");
  let scenario = $state("");
  let selectedStage = $state("");
  let meetingScope = "";
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
  const template = $derived(data.templates.find((t) => t.id === data.meeting?.template));
  const stages = $derived(template?.instructor?.stages ?? []);
  const otherBreaks = $derived(stages.length ? (data.exercises ?? []).filter(d => !stages.some(s => s.exerciseID === d.id)) : []);
  const stage = $derived(stages.find((s) => s.id === selectedStage) ?? stages[0]);
  $effect(() => {
    const id = data.meeting?.id ?? "";
    if (id !== meetingScope) { meetingScope = id; selected = []; selectedStage = ""; scenario = stages[0]?.exerciseID ?? ""; error = ""; }
  });
  function chooseStage(id: string) {
    selectedStage = id;
    scenario = stages.find((s) => s.id === id)?.exerciseID ?? "";
    error = "";
  }
  async function checkServices() {
    busy = true; error = "";
    try { await api("/api/admin/checks/run", {}); await onchange(); }
    catch(e) { error = (e as Error).message; }
    finally { busy = false; }
  }
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

{#if (data.exercises?.length || stages.length) && open}
  <section aria-labelledby="exercises-title">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2
        id="exercises-title"
        class="text-sm font-semibold"
      >
        Exercises
      </h2>
      {#if data.exercises?.length}<div class="flex flex-wrap items-center gap-2">
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
      </div>{/if}
    </div>

    <Notice message={error} dismissible />
    {#if stages.length}
      <div class="mt-5 grid items-start gap-6 lg:grid-cols-[minmax(12rem,17rem)_minmax(0,1fr)]">
        <div class="min-w-0">
          <label class="label mb-2 text-sm" for="meeting-stage">Template stage</label>
          <select id="meeting-stage" class="select select-sm min-h-11 w-full" value={stage?.id} disabled={busy} onchange={(e) => chooseStage(e.currentTarget.value)}>
            {#each stages as s, i (s.id)}<option value={s.id}>{i + 1}. {s.title}</option>{/each}
          </select>
          
          <a class="link mt-3 inline-flex items-center gap-2 text-xs" href={`/labs/?template=${encodeURIComponent(template?.id ?? "")}`}><BookOpen size={14} />Student preview</a>
        </div>
        {#if stage}<article class="min-w-0 border-t border-base-content/10 pt-5 lg:border-t-0 lg:border-l lg:pt-0 lg:pl-6" aria-label="Meeting stage guidance">
          <h3 class="text-lg font-semibold">{stage.title}</h3>
          <div class={[prose, "mt-3 break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto"]}>{@html render(stage.instructions)}</div>
          {#if stage.prompt}<p class="mt-3 text-sm leading-relaxed">{stage.prompt}</p>{/if}
          {#key stage.id}{#if stage.solution || stage.answer}<details class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mt-4">
            <summary class="collapse-title flex items-center gap-2 text-sm font-medium"><LockKeyhole size={14} />Instructor solution</summary>
            <div class="collapse-content">{#if stage.solution}<div class={prose}>{@html render(stage.solution)}</div>{/if}{#if stage.answer}<p class="mt-3 text-sm">Accepted answer: <code>{stage.answer}</code></p>{/if}</div>
          </details>{/if}{/key}
          <p class="mt-3 text-xs text-base-content/60">{stage.exerciseID ? `Break: ${data.exercises.find(d => d.id === stage.exerciseID)?.name ?? stage.exerciseID}. Apply it separately to selected teams.` : `This stage has no break to apply.${template?.instructor?.checks.length ? " Service checks remain available." : ""}${data.exercises?.length ? " You can reset the exercise baseline separately." : ""}`}</p>
        </article>{/if}
      </div>
    {:else}
      <div class="mt-4 w-full sm:w-72">
        <label class="label mb-2" for="exercise-scenario">Exercise</label>
        <select class="select select-sm min-h-11 w-full" id="exercise-scenario" bind:value={scenario} disabled={busy}>
          <option value="" disabled>Choose an exercise</option>{#each data.exercises as d}<option value={d.id}>{d.name}</option>{/each}
        </select>
      </div>
    {/if}
    {#if otherBreaks.length}<details class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mt-4">
      <summary class="collapse-title text-sm font-medium">Other template breaks · {otherBreaks.length}</summary>
      <div class="collapse-content">
        <label class="label mb-2 text-sm" for="other-template-break">Other template break</label>
        <select id="other-template-break" class="select select-sm min-h-11 w-full" disabled={busy} value={otherBreaks.some(d => d.id === scenario) ? scenario : ""} onchange={(e) => scenario = e.currentTarget.value}>
          <option value="" disabled>Choose another break</option>{#each otherBreaks as d (d.id)}<option value={d.id}>{d.name}</option>{/each}
        </select>
        <p class="mt-2 text-xs leading-relaxed text-base-content/60">These pinned breaks are independent of the authored stages. Selecting a stage again restores its linked break.</p>
      </div>
    </details>{/if}
    {#if definition}<details
        class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mt-4"
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
    {#if data.exercises?.length}<fieldset class="mt-4">
      <legend class="text-sm font-medium">Teams</legend>
      <div class="mt-2 divide-y divide-base-content/10">
        {#each data.teams as team}
          {@const record = data.exerciseRuns?.find((r) => r.teamID === team.id)}
          <div class="py-1">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <label class="flex min-h-11 items-center gap-3"
                ><input
                  class="checkbox checkbox-sm scroll-mb-40 lg:scroll-mb-24"
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
    </p>{/if}
    <div
      role="group"
      aria-label="Exercise actions"
      class="sticky bottom-[calc(4rem+1px+env(safe-area-inset-bottom))] z-10 mt-4 border-t border-base-content/10 bg-base-100 py-3 lg:bottom-0 lg:py-4"
    >
      <div class="flex flex-wrap items-center gap-2">
        {#if data.exercises?.length}<button
          class="btn btn-sm min-h-11 btn-primary"
          disabled={busy || !scenario || !chosen.length}
          onclick={() => confirm(scenario)}
          ><Play size={16} />{stages.length ? "Apply break to" : "Run on"} {chosen.length}
          {chosen.length === 1 ? "team" : "teams"}</button
        ><button
          class="btn btn-sm min-h-11"
          disabled={busy || !chosen.length}
          onclick={() => confirm("reset")}
          ><RotateCcw size={16} />{custom
            ? "Reset exercise baseline"
            : "Reset web server"}</button
        >{/if}{#if custom && template?.instructor?.checks.length}<button
          class="btn btn-sm min-h-11" title="Runs all pinned checks on every ready team" disabled={busy || !data.teams.some(t => t.status === "ready")} onclick={checkServices}
          ><Check size={16} />Check services</button
        >{/if}{#if !custom}<button
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
    <details class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mt-3">
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
