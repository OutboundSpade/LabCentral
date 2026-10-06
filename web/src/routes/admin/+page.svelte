<script lang="ts">
  import Activity from "$lib/Activity.svelte";
  import Scoreboard from "$lib/Scoreboard.svelte";
  import Exercises from "$lib/Exercises.svelte";
  import Status from "$lib/Status.svelte";
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import PageHeader from "$lib/PageHeader.svelte";
  import {
    Clock,
    Play,
    Plus,
    Trash2,
    Users,
    Monitor,
    Activity as ActivityIcon,
    Shield,
    ListChecks,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import Notice from "$lib/Notice.svelte";
  import { api, adminView, refreshAdmin, date } from "$lib/api";
  let data = $derived($adminView);
  let error = $state("");
  let busy = $state(false);
  let name = $state("Example Branch practice");
  let template = $state("");
  let hours = $state(4);
  let teamCount = $state(5);
  let capacity = $state(6);
  let extension = $state(1);
  const readyCount = $derived(
    data?.teams.filter((t) => t.status === "ready").length ?? 0,
  );
  const failedCount = $derived(
    data?.teams.filter((t) => t.status === "failed").length ?? 0,
  );
  const preparing = $derived(
    data?.jobs.some(
      (j) => j.kind === "prepare" && ["queued", "running"].includes(j.status),
    ) ?? false,
  );
  const remainingHours = $derived(
    data?.meeting
      ? Math.max(
          0,
          Math.floor(
            (Date.parse(data.meeting.created) +
              8 * 3600000 -
              Date.parse(data.meeting.expires)) /
              3600000,
          ),
        )
      : 0,
  );
  $effect(() => {
    if (remainingHours > 0 && extension > remainingHours)
      extension = remainingHours;
  });
  let confirmation: HTMLDialogElement;
  let action = $state("");
  async function load() {
    try {
      await refreshAdmin();
      if (!template)
        template = $adminView?.templates.find((t) => t.built)?.id ?? "";
    } catch (e) {
      error = (e as Error).message;
    }
  }
  onMount(() => {
    void load();
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
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
  function confirm(which: string) {
    action = which;
    confirmation.showModal();
  }
  const panel = $derived(
    ["teams", "exercises", "scoreboard"].includes(page.url.hash.slice(1))
      ? page.url.hash.slice(1)
      : "teams",
  );
  const hasChecks = $derived(
    !!data?.templates.find((t) => t.id === data.meeting?.template)?.instructor
      ?.checks.length,
  );
  const tabs = $derived([
    { id: "teams", label: "Teams", icon: Users },
    ...(data?.exercises?.length
      ? [{ id: "exercises", label: "Exercises", icon: Shield }]
      : []),
    ...(hasChecks
      ? [{ id: "scoreboard", label: "Scoreboard", icon: ListChecks }]
      : []),
  ]);
  const visiblePanel = $derived(
    tabs.some((t) => t.id === panel) ? panel : "teams",
  );
  function choosePanel(id: string) {
    void goto("#" + id, { keepFocus: true, noScroll: true });
  }
  function tabKey(event: KeyboardEvent) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const index = tabs.findIndex((t) => t.id === visiblePanel);
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? tabs.length - 1
          : (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) %
            tabs.length;
    choosePanel(tabs[next].id);
    document.getElementById("meeting-tab-" + tabs[next].id)?.focus();
  }
</script>

<svelte:head><title>Manage meeting · LabCentral</title></svelte:head>
<PageHeader title="Manage meeting" />
<Guard admin
  ><Notice message={error} />{#if data}
    {#if !data.meeting || data.meeting.status === "ended"}
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]"
      >
        <section class="min-w-0">
          <form
            class="max-w-xl"
            onsubmit={(e) => {
              e.preventDefault();
              void run("/api/admin/meeting", {
                name,
                template,
                hours,
                teamCount,
                capacity,
              });
            }}
          >
            <fieldset class="fieldset gap-2 text-sm" disabled={busy}>
              <legend class="fieldset-legend text-base">Create a meeting</legend
              >
              <label class="label" for="meeting-name">Meeting name</label><input
                class="input input-sm min-h-11 w-full"
                id="meeting-name"
                bind:value={name}
                maxlength="100"
                required
              />
              <label class="label mt-3" for="template"
                >Built template revision</label
              ><select
                id="template"
                class="select select-sm min-h-11 w-full"
                bind:value={template}
                required
                ><option value="" disabled>Choose a built revision</option
                >{#each data.templates.filter((t) => t.built) as t}<option
                    value={t.id}>{t.name} · {t.commit.slice(0, 7)}</option
                  >{/each}</select
              >
              <div class="mt-3 grid gap-4 sm:grid-cols-3">
                <div>
                  <label class="label mb-2" for="team-count"
                    >Number of teams</label
                  ><input
                    id="team-count"
                    class="input input-sm min-h-11 w-full"
                    type="number"
                    min="1"
                    max="100"
                    step="1"
                    bind:value={teamCount}
                    required
                    aria-describedby="team-count-help"
                  />
                  <p
                    id="team-count-help"
                    class="mt-2 text-xs text-base-content/75"
                  >
                    1–100 teams
                  </p>
                </div>
                <div>
                  <label class="label mb-2" for="capacity"
                    >Students per team</label
                  ><input
                    id="capacity"
                    class="input input-sm min-h-11 w-full"
                    type="number"
                    min="1"
                    max="100"
                    step="1"
                    bind:value={capacity}
                    required
                    aria-describedby="capacity-help"
                  />
                  <p
                    id="capacity-help"
                    class="mt-2 text-xs text-base-content/75"
                  >
                    1–100 students
                  </p>
                </div>
                <div>
                  <label class="label mb-2" for="duration"
                    >Duration (hours)</label
                  ><input
                    id="duration"
                    class="input input-sm min-h-11 w-full"
                    type="number"
                    min="1"
                    max="8"
                    step="1"
                    bind:value={hours}
                    required
                    aria-describedby="duration-help"
                  />
                  <p
                    id="duration-help"
                    class="mt-2 text-xs text-base-content/75"
                  >
                    1–8 hours
                  </p>
                </div>
              </div>
              <p class="mt-3 text-sm leading-relaxed text-base-content/75">
                {(teamCount || 0) * (capacity || 0)} student places. Machines and
                files are deleted automatically when the meeting ends.
              </p>
              <button
                disabled={busy || !template}
                class="btn btn-sm min-h-11 btn-primary mt-3 w-fit"
                >{#if busy}<span class="loading loading-spinner loading-xs"
                  ></span>{:else}<Plus size={16} />{/if}Create meeting</button
              >
            </fieldset>
          </form>
          {#if !data.templates.some((t) => t.built)}<p class="mt-4 text-sm">
              First, <a class="link" href="/admin/templates/"
                >build a template</a
              >.
            </p>{/if}
        </section>
        <aside class="min-w-0 lg:border-l lg:border-base-content/10 lg:pl-6">
          <Activity {data} onchange={refreshAdmin} />
        </aside>
      </div>
    {:else}
      <section
        class="mb-6 border-b border-base-content/10 pb-5"
        aria-label="Current meeting"
      >
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-3">
              <h2 class="text-xl font-semibold tracking-tight">
                {data.meeting.name}
              </h2>
              <Status status={data.meeting.status} />
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            {#if data.meeting.status !== "ending"}
              {#if remainingHours > 0}<div class="flex items-center gap-2">
                  <label class="sr-only" for="extension">Extend by</label
                  ><select
                    id="extension"
                    class="select select-sm min-h-11 w-28 lg:min-h-8"
                    bind:value={extension}
                    disabled={busy}
                    >{#each Array.from({ length: remainingHours }, (_, i) => i + 1) as amount}<option
                        value={amount}
                        >{amount} {amount === 1 ? "hour" : "hours"}</option
                      >{/each}</select
                  ><button
                    type="button"
                    class="btn btn-sm min-h-11 lg:min-h-8"
                    disabled={busy}
                    onclick={() =>
                      run("/api/admin/extend", { hours: extension })}
                    ><Plus size={15} />Add time</button
                  >
                </div>{:else}<span class="text-xs text-base-content/75"
                  >Eight-hour limit reached</span
                >{/if}
              <button
                class="btn btn-sm min-h-11 btn-primary lg:min-h-8"
                disabled={busy || readyCount === data.teams.length || preparing}
                onclick={() => confirm("prepare")}
                ><Play size={15} />Prepare labs</button
              >
              <button
                type="button"
                class="btn btn-sm min-h-11 btn-ghost text-error lg:min-h-8"
                disabled={busy}
                onclick={() => confirm("end")}
                ><Trash2 size={15} />End meeting</button
              >
            {/if}
          </div>
        </div>
        <div
          class="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs tabular-nums text-base-content/75"
        >
          <span class="inline-flex items-center gap-1.5"
            ><Clock size={14} />Cleanup at {date(data.meeting.expires)}</span
          ><span class="inline-flex items-center gap-1.5"
            ><Users size={14} />{readyCount} / {data.teams.length} labs ready · {data.teams.reduce(
              (total, team) => total + team.capacity,
              0,
            )} student places</span
          ><span class="inline-flex items-center gap-1.5"
            ><Monitor size={14} />{data.activeDesktops ?? 0} active desktops</span
          ><a
            class="link link-hover inline-flex min-h-8 items-center gap-1.5"
            href="#activity-title"
            ><ActivityIcon size={14} />{data.jobs.filter(
              (j) => j.status === "running",
            ).length} running · {data.jobs.filter((j) => j.status === "queued")
              .length} queued</a
          >
        </div>
        {#if failedCount}<p class="mt-3 text-sm text-error">
            {failedCount}
            {failedCount === 1 ? "lab needs" : "labs need"} attention.
            <a class="link" href="#activity-title">View activity</a>
          </p>{/if}
        {#if readyCount === data.teams.length}<p
            class="mt-2 text-xs text-base-content/75"
          >
            All team labs are prepared.
          </p>{:else if preparing}<p class="mt-2 text-xs text-base-content/75">
            Preparation is running. Ready teams can use their labs.
          </p>{/if}
      </section>
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]"
      >
        <section class="min-w-0">
          <div
            role="tablist"
            aria-label="Meeting tools"
            class="tabs tabs-border mb-5"
          >
            {#each tabs as tab}<button
                type="button"
                role="tab"
                id={"meeting-tab-" + tab.id}
                aria-controls={"meeting-panel-" + tab.id}
                aria-selected={visiblePanel === tab.id}
                tabindex={visiblePanel === tab.id ? 0 : -1}
                class="tab min-h-11 gap-2 text-sm"
                class:tab-active={visiblePanel === tab.id}
                class:text-primary={visiblePanel === tab.id}
                onclick={() => choosePanel(tab.id)}
                onkeydown={tabKey}><tab.icon size={15} />{tab.label}</button
              >{/each}
          </div>
          <div
            role="tabpanel"
            id="meeting-panel-teams"
            aria-labelledby="meeting-tab-teams"
            hidden={visiblePanel !== "teams"}
          >
            <div class="max-w-full overflow-x-auto">
              <table class="table table-sm">
                <thead><tr><th>Team</th><th>Members</th><th>Lab</th></tr></thead
                ><tbody
                  >{#each data.teams as team}<tr class="border-base-content/10"
                      ><th class="font-medium"
                        ><span class="flex items-center gap-2"
                          ><Users
                            size={15}
                            class="text-base-content/75"
                          />{team.name}</span
                        ></th
                      ><td class="tabular-nums text-base-content/75"
                        >{team.members} / {team.capacity}</td
                      ><td><Status status={team.status} /></td></tr
                    >{/each}</tbody
                >
              </table>
            </div>
          </div>
          {#if data.exercises?.length}<div
              role="tabpanel"
              id="meeting-panel-exercises"
              aria-labelledby="meeting-tab-exercises"
              hidden={visiblePanel !== "exercises"}
            >
              <Exercises {data} onchange={load} />
            </div>{/if}
          {#if hasChecks}<div
              role="tabpanel"
              id="meeting-panel-scoreboard"
              aria-labelledby="meeting-tab-scoreboard"
              hidden={visiblePanel !== "scoreboard"}
            >
              <Scoreboard {data} onchange={load} />
            </div>{/if}
        </section>
        <aside
          class="min-w-0 lg:sticky lg:top-6 lg:border-l lg:border-base-content/10 lg:pl-6"
        >
          <Activity {data} onchange={refreshAdmin} />
        </aside>
      </div>
    {/if}
  {:else}<p
      class="flex items-center gap-2 text-sm text-base-content/75"
      role="status"
    >
      <span class="loading loading-spinner loading-sm"></span>Loading meeting…
    </p>{/if}</Guard
>
<dialog bind:this={confirmation} class="modal" aria-labelledby="confirm-title">
  <div class="modal-box">
    <h2 id="confirm-title" class="text-lg font-semibold">
      {action === "end" ? "End this meeting?" : "Prepare the team labs?"}
    </h2>
    <p class="mt-3 font-medium">{data?.meeting?.name}</p>
    <p class="mt-3 text-sm leading-relaxed">
      {action === "end"
        ? `This deletes all ${data?.teams.length ?? 0} team labs, including their machines and workspace files. This cannot be undone.`
        : `This provisions the selected template for ${data?.teams.length ?? 0} teams. Cloud compute, storage, public addresses, and transfer incur charges. Machines and files are deleted at the meeting deadline.`}
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11">Cancel</button>
      </form>
      <button
        class="btn btn-sm min-h-11"
        class:btn-error={action === "end"}
        class:btn-primary={action === "prepare"}
        disabled={busy}
        onclick={() => run("/api/admin/" + action)}
        >{action === "end" ? "Delete team labs" : "Prepare labs"}</button
      >
    </div>
  </div>
</dialog>
