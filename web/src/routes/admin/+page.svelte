<script lang="ts">
  import CoachConsole from "$lib/CoachConsole.svelte";
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
  import { LibraryBig, Play, Plus, Trash2, Users } from "@lucide/svelte";
  import EmptyState from "$lib/EmptyState.svelte";
  import Guard from "$lib/Guard.svelte";
  import Notice from "$lib/Notice.svelte";
  import { api, adminView, refreshAdmin, date, session } from "$lib/api";
  let data = $derived($adminView);
  let error = $state("");
  let busy = $state(false);
  let extension = $state(1);
  let success = $state("");
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
  const hasExercises = $derived(!!(data?.breaks?.length || data?.templates.find(t => t.id === data.meeting?.template)?.instructor?.stages?.length));
  const tabs = $derived([
    { id: "teams", label: "Teams" },
    ...(hasExercises ? [{ id: "exercises", label: "Exercises" }] : []),
    ...(hasChecks ? [{ id: "scoreboard", label: "Scoreboard" }] : []),
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
<Guard admin
  ><Notice message={error} dismissible /><PollStatus />
  {#if success}<p class="mb-4 text-sm text-success" role="status">{success}</p>{/if}
  {#if data}
    {#if !data.meeting || data.meeting.status === "ended"}
      <PageHeader title="Meeting" />
      <AdminNav />
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-12"
      >
        <section class="min-w-0">
          <EmptyState icon={Users} title="No meeting running">
            Start one from a template.
            {#snippet actions()}<a class="btn btn-sm btn-primary min-h-11" href="/admin/templates/"
                ><LibraryBig size={15} />Choose a template</a
              >{/snippet}
          </EmptyState>
        </section>
        <aside class="min-w-0">
          <Activity {data} onchange={refreshAdmin} />
        </aside>
      </div>
    {:else}
      <PageHeader title={data.meeting.name}>
        {#snippet eyebrow()}<span class="inline-flex items-center gap-2"
            >Meeting <Status status={data?.meeting?.status ?? ""} /></span
          >{/snippet}
        {#if data.meeting.status !== "ending"}
          {#if remainingHours > 0}<div class="join">
              <label class="sr-only" for="extension">Extend by</label
              ><select
                id="extension"
                class="select select-sm join-item min-h-11 w-24 lg:min-h-8"
                bind:value={extension}
                disabled={busy}
                >{#each Array.from({ length: remainingHours }, (_, i) => i + 1) as amount}<option
                    value={amount}
                    >{amount} {amount === 1 ? "hour" : "hours"}</option
                  >{/each}</select
              ><button
                type="button"
                class="btn btn-sm join-item min-h-11 lg:min-h-8"
                disabled={busy}
                onclick={() => run("/api/admin/extend", { hours: extension })}
                ><Plus size={15} />Add time</button
              >
            </div>{:else}<span class="text-xs text-base-content/60"
              >Eight-hour limit reached</span
            >{/if}
          {#if readyCount !== data.teams.length}<button
              class="btn btn-sm min-h-11 btn-primary lg:min-h-8"
              disabled={busy || preparing}
              onclick={() => confirm("prepare")}
              ><Play size={15} />Prepare labs</button
            >{/if}
          <button
            type="button"
            class="btn btn-sm min-h-11 btn-error btn-soft lg:min-h-8"
            disabled={busy}
            onclick={() => confirm("end")}
            ><Trash2 size={15} />End meeting</button
          >
        {/if}
      </PageHeader>
      <AdminNav />
      <section
        class="rounded-box border border-base-content/9 bg-base-200/70 mb-8 grid grid-cols-2 divide-base-content/8 sm:grid-cols-4 sm:divide-x"
        aria-label="Meeting overview"
      >
        <div class="px-5 py-4">
          <p class="text-xs font-medium text-base-content/70">Time left</p>
          <div class="mt-1">
            {#if data.meeting.status === "ending"}<p class="text-sm text-warning">
                Cleaning up…
              </p>{:else}<Deadline
                expires={data.meeting.expires}
                label="Cleanup"
                compact
              />{/if}
          </div>
        </div>
        <div class="px-5 py-4">
          <p class="text-xs font-medium text-base-content/70">Labs ready</p>
          <p class="mt-1 text-lg font-semibold tabular-nums">
            {readyCount} / {data.teams.length}
          </p>
        </div>
        <div class="px-5 py-4">
          <p class="text-xs font-medium text-base-content/70">Students joined</p>
          <p class="mt-1 text-lg font-semibold tabular-nums">
            {joined} / {seats}
          </p>
        </div>
        <div class="px-5 py-4">
          <p class="text-xs font-medium text-base-content/70">Active desktops</p>
          <p class="mt-1 text-lg font-semibold tabular-nums">
            {data.activeDesktops ?? 0}
          </p>
        </div>
      </section>
      {#if failedCount}<p class="-mt-4 mb-6 text-sm text-error">
          {failedCount}
          {failedCount === 1 ? "lab needs" : "labs need"} attention.
          <a class="link" href="#activity-title">View activity</a>
        </p>{:else if preparing}<p class="-mt-4 mb-6 text-sm text-base-content/70">
          Preparing labs. Ready teams can already use theirs.
        </p>{:else if data.meeting.status === "waiting" && !readyCount}<p
          class="-mt-4 mb-6 text-sm text-base-content/70"
        >
          Prepare labs to open them to teams.
        </p>{/if}
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-12"
      >
        <section class="min-w-0">
          {#if tabs.length === 1}<h2 class="mb-4 text-sm font-semibold">Teams</h2>{:else}<div
            role="tablist"
            aria-label="Meeting tools"
            class="mb-5 flex gap-5 border-b border-base-content/8"
          >
            {#each tabs as tab}<button
                type="button"
                role="tab"
                id={"meeting-tab-" + tab.id}
                aria-controls={"meeting-panel-" + tab.id}
                aria-selected={visiblePanel === tab.id}
                tabindex={visiblePanel === tab.id ? 0 : -1}
                class={[
                  "-mb-px min-h-11 border-b-2 text-sm focus-visible:outline-2 focus-visible:outline-primary",
                  visiblePanel === tab.id
                    ? "border-primary font-medium"
                    : "border-transparent text-base-content/70 hover:text-base-content",
                ]}
                onclick={() => choosePanel(tab.id)}
                onkeydown={tabKey}>{tab.label}</button
              >{/each}
          </div>{/if}
          <div
            role={tabs.length > 1 ? "tabpanel" : undefined}
            id="meeting-panel-teams"
            aria-labelledby={tabs.length > 1 ? "meeting-tab-teams" : undefined}
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
                  ><tr class="border-base-content/8 text-base-content/60"
                    ><th scope="col">Team</th><th scope="col">Members</th><th
                      scope="col">Desktops</th
                    ><th scope="col">Lab</th></tr
                  ></thead
                ><tbody>
                  {#each data.teams as team}<tr class="border-base-content/8"
                      ><th scope="row" class="whitespace-nowrap font-medium"
                        >{team.name}</th
                      ><td class="whitespace-nowrap tabular-nums"
                        >{team.members} / {team.capacity}</td
                      ><td class="tabular-nums"
                        >{data.teamDesktops?.find((d) => d.teamID === team.id)
                          ?.count ?? 0}</td
                      ><td
                        ><Status
                          status={team.status}
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
            <CoachConsole {data} />
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
          class="min-w-0 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary lg:sticky lg:top-20 lg:max-h-[calc(100dvh-6rem)] lg:overflow-y-auto lg:overscroll-contain lg:pr-1"
          aria-labelledby="activity-title"
          tabindex="0"
        >
          <Activity {data} onchange={refreshAdmin} />
        </aside>
      </div>
    {/if}
  {:else}<p
      class="flex items-center gap-2 text-sm text-base-content/70"
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
        ? `Deletes all ${data?.teams.length ?? 0} team labs, including machines and workspace files. This cannot be undone.`
        : `Provisions ${data?.teams.length ?? 0} team labs. Cloud resources incur charges until the meeting ends.`}
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
