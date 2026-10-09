<script lang="ts">
  import Activity from "$lib/Activity.svelte";
  import Scoreboard from "$lib/Scoreboard.svelte";
  import Exercises from "$lib/Exercises.svelte";
  import Status from "$lib/Status.svelte";
  import Deadline from "$lib/Deadline.svelte";
  import PollStatus from "$lib/PollStatus.svelte";
  import type { Meeting } from "$lib/types";
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import PageHeader from "$lib/PageHeader.svelte";
  import AdminNav from "$lib/AdminNav.svelte";
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
  import { api, adminView, refreshAdmin, date, session } from "$lib/api";
  let data = $derived($adminView);
  let error = $state("");
  let busy = $state(false);
  let name = $state("Example Branch practice");
  let template = $state("");
  let hours = $state(4);
  let teamCount = $state(5);
  let capacity = $state(6);
  let extension = $state(1);
  let success = $state("");
  let createdID = $state("");
  let selectionRequest = $state<string | null>(null);
  let lostRevision = $state(false);
  const requestedRevision = $derived(page.url.searchParams.get("template") ?? "");
  const builtRevisions = $derived(
    (data?.templates.filter((t) => t.built && !t.removing) ?? []).toSorted(
      (a, b) =>
        Date.parse(b.committedAt ?? b.importedAt ?? "") -
        Date.parse(a.committedAt ?? a.importedAt ?? ""),
    ),
  );
  const selectedRevision = $derived(builtRevisions.find((t) => t.id === template));
  const revisionNotice = $derived(
    !selectedRevision && data
      ? lostRevision
        ? "The selected revision is no longer available for a meeting. Explicitly choose an available built revision."
        : requestedRevision
          ? data.templates.some((t) => t.id === requestedRevision && !t.built && !t.removing)
            ? "The requested revision needs its images built before a meeting can use it. Build this revision in template administration, or explicitly choose another built revision."
            : "The requested revision is unavailable for a meeting. It may have been removed or be awaiting removal. Explicitly choose an available built revision."
          : ""
      : "",
  );
  $effect(() => {
    if (!data) return;
    if (selectionRequest !== requestedRevision) {
      selectionRequest = requestedRevision;
      lostRevision = false;
      template = requestedRevision
        ? builtRevisions.find((t) => t.id === requestedRevision)?.id ?? ""
        : builtRevisions[0]?.id ?? "";
    } else if (template && !selectedRevision) {
      template = "";
      lostRevision = true;
    }
  });
  const joined = $derived(data?.joinedStudents ?? 0);
  const seats = $derived(
    data?.teams.reduce((sum, t) => sum + t.capacity, 0) ?? 0,
  );
  const running = $derived(
    data?.jobs.filter((j) => j.status === "running").length ?? 0,
  );
  const queued = $derived(
    data?.jobs.filter((j) => j.status === "queued").length ?? 0,
  );
  const failures = $derived(
    data?.jobs.filter((j) => j.status === "failed" && j.needsAttention)
      .length ?? 0,
  );
  const prepareFailure = $derived(
    data?.jobs.find(
      (j) =>
        j.kind === "prepare" &&
        j.target === data.meeting?.id &&
        j.status === "failed",
    ),
  );
  function failureFor(name: string) {
    const lines = prepareFailure?.error?.split("\n") ?? [];
    return (
      lines
        .find((line) => line.startsWith(name + ": "))
        ?.slice(name.length + 2) ??
      prepareFailure?.error ??
      "Preparation failed. See live activity for recovery details."
    );
  }
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
    await refreshAdmin().catch(() => {});
  }
  onMount(() => {
    void load();
  });
  async function run(path: string, body: unknown = {}) {
    busy = true;
    error = "";
    success = "";
    try {
      const result = await api<Meeting>(path, body);
      if (path === "/api/admin/extend")
        success = `Extended to ${date(result.expires)}.`;
      if (path === "/api/admin/meeting") {
        createdID = result.id;
        success =
          "Meeting created. Prepare labs now to make the teams available.";
      }
      if (path === "/api/admin/jobs/retry")
        success = "Preparation recovery queued. Ready team labs are preserved.";
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
  const hasExercises = $derived(!!(data?.exercises?.length || data?.templates.find(t => t.id === data.meeting?.template)?.instructor?.stages?.length));
  const tabs = $derived([
    { id: "teams", label: "Teams", icon: Users },
    ...(hasExercises
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
{#if $session?.user.admin}<AdminNav />{/if}
<PageHeader title="Manage meeting" />
<Guard admin
  ><Notice message={error} dismissible /><PollStatus />
  {#if success}<p class="mb-4 text-sm" role="status">{success}</p>{/if}
  {#if data}
    {#if !data.meeting || data.meeting.status === "ended"}
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]"
      >
        <section class="min-w-0">
          <form
            class="max-w-xl"
            onsubmit={(e) => {
              e.preventDefault();
              if (!selectedRevision) {
                error = "Choose an available built template revision.";
                return;
              }
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
                aria-describedby={revisionNotice ? "revision-notice" : undefined}
                ><option value="" disabled>Choose a built revision</option
                >{#each builtRevisions as t}<option value={t.id}
                    >{t.name} · {t.commitTitle || "Imported revision"} · {t.commit.slice(
                      0,
                      7,
                    )} · {t.committedAt
                      ? date(t.committedAt, { dateStyle: "medium" })
                      : "Date unavailable"}</option
                  >{/each}</select
              >
              {#if revisionNotice}<p id="revision-notice" class="mt-2 text-sm leading-relaxed text-warning" role="status">{revisionNotice} <a class="link" href="/admin/templates/">Template administration</a></p>{/if}
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
                  <p id="team-count-help" class="mt-2 text-sm">1–100 teams</p>
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
                  <p id="capacity-help" class="mt-2 text-sm">1–100 students</p>
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
                  <p id="duration-help" class="mt-2 text-sm">1–8 hours</p>
                </div>
              </div>
              <p class="mt-3 text-sm font-medium">
                {(teamCount || 0) * (capacity || 0)} desktop places · {teamCount ||
                  0} team {teamCount === 1 ? "lab" : "labs"} · {hours || 0}
                {hours === 1 ? "hour" : "hours"}
              </p>
              <p class="text-sm leading-relaxed">
                Machines and files are deleted automatically when the meeting
                ends. Cloud compute and storage incur charges.
              </p>
              <button
                disabled={busy || !selectedRevision}
                class="btn btn-sm min-h-11 btn-primary mt-3 w-fit"
                >{#if busy}<span class="loading loading-spinner loading-xs"
                  ></span>{:else}<Plus size={16} />{/if}Create meeting</button
              >
            </fieldset>
          </form>
          {#if !builtRevisions.length}<p class="mt-4 text-sm">
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
                ><Play size={15} />{createdID === data.meeting.id &&
                data.meeting.status === "waiting"
                  ? "Prepare labs now"
                  : "Prepare labs"}</button
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
        <div class="mt-4">
          {#if data.meeting.status === "ending"}<p class="text-sm text-warning">
              Cleanup is in progress. Team machines and workspace files are
              being removed.
            </p>{:else}<Deadline
              expires={data.meeting.expires}
              label="Cleanup at"
            />{/if}
        </div>
        <div
          class="stats mt-4 grid w-full grid-flow-row grid-cols-2 overflow-visible rounded-field border border-base-content/10 bg-base-200/40 sm:grid-cols-4"
          aria-label="Meeting overview"
        >
          <div class="stat min-w-0 px-3 py-3">
            <div class="stat-title text-xs text-base-content/75">
              Labs ready
            </div>
            <div class="stat-value text-xl tabular-nums">
              {readyCount} / {data.teams.length}
            </div>
          </div>
          <div class="stat min-w-0 px-3 py-3">
            <div class="stat-title text-xs text-base-content/75">
              Students joined
            </div>
            <div class="stat-value text-xl tabular-nums">
              {joined} / {seats}
            </div>
            <div class="stat-desc whitespace-normal text-xs">Seats filled</div>
          </div>
          <div class="stat min-w-0 px-3 py-3">
            <div class="stat-title text-xs text-base-content/75">
              Active desktops
            </div>
            <div class="stat-value text-xl tabular-nums">
              {data.activeDesktops ?? 0}
            </div>
          </div>
          <div class="stat min-w-0 px-3 py-3">
            <div class="stat-title text-xs text-base-content/75">
              Operations
            </div>
            <a
              class="stat-value link link-hover text-xl tabular-nums"
              href="#activity-title">{running} running</a
            >
            <div class="stat-desc whitespace-normal text-xs">
              {queued} queued · {failures} failed needing attention
            </div>
          </div>
        </div>
        {#if failedCount}<p class="mt-3 text-sm text-error">
            {failedCount}
            {failedCount === 1 ? "lab needs" : "labs need"} attention.
            <a class="link" href="#activity-title">View activity</a>
          </p>{/if}
        {#if readyCount === data.teams.length}<p class="mt-2 text-sm">
            All team labs are prepared.
          </p>{:else if preparing}<p class="mt-2 text-sm">
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
            <!-- Keyboard scrolling keeps all team columns accessible on phones. -->
            <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
            <div
              class="max-w-full overflow-x-auto"
              tabindex="0"
              role="region"
              aria-label="Team labs table"
            >
              <table class="table table-sm">
                <caption class="sr-only"
                  >Team lab readiness and desktop sessions</caption
                ><thead
                  ><tr
                    ><th scope="col">Team</th><th scope="col">Members</th><th
                      scope="col">Desktops</th
                    ><th scope="col">Lab</th></tr
                  ></thead
                ><tbody>
                  {#each data.teams as team}<tr class="border-base-content/10"
                      ><th scope="row" class="whitespace-nowrap font-medium"
                        ><span class="flex items-center gap-2"
                          ><Users
                            size={15}
                            class="text-base-content/75"
                          />{team.name}</span
                        ></th
                      ><td class="whitespace-nowrap tabular-nums"
                        >{team.members} / {team.capacity}</td
                      ><td class="tabular-nums"
                        >{data.teamDesktops?.find((d) => d.teamID === team.id)
                          ?.count ?? 0}</td
                      ><td
                        ><Status
                          status={team.status}
                          soft
                        />{#if team.status === "failed"}<p
                            class="mt-2 min-w-48 whitespace-pre-line text-sm text-error"
                          >
                            {failureFor(team.name)}
                          </p>
                          {#if prepareFailure?.retryable}<button
                              class="btn btn-sm min-h-11 mt-2"
                              disabled={busy || preparing}
                              onclick={() =>
                                run("/api/admin/jobs/retry", {
                                  id: prepareFailure?.id,
                                })}>Retry failed labs</button
                            >{:else if prepareFailure?.retryBlocked}<p
                              class="mt-2 text-sm"
                            >
                              {prepareFailure.retryBlocked}
                            </p>{/if}{/if}</td
                      ></tr
                    >{/each}
                </tbody>
              </table>
            </div>
          </div>
          {#if hasExercises}<div
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
        <!-- On short laptop screens the pinned activity feed scrolls on its own. -->
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <aside
          class="min-w-0 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary lg:sticky lg:top-6 lg:max-h-[calc(100dvh-3rem)] lg:overflow-y-auto lg:overscroll-contain lg:border-l lg:border-base-content/10 lg:pr-1 lg:pl-6"
          aria-labelledby="activity-title"
          tabindex="0"
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
        <button class="btn btn-sm min-h-11" disabled={busy}>Cancel</button>
      </form>
      <button
        class="btn btn-sm min-h-11"
        class:btn-error={action === "end"}
        class:btn-primary={action === "prepare"}
        disabled={busy}
        onclick={() => run("/api/admin/" + action)}
        >{#if busy}<span class="loading loading-spinner loading-xs"
          ></span>{/if}{action === "end"
          ? "Delete team labs"
          : "Prepare labs"}</button
      >
    </div>
  </div>
</dialog>
