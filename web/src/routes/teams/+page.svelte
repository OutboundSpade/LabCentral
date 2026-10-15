<script lang="ts">
  import { goto } from "$app/navigation";
  import { Users, ArrowRight, LogOut, RotateCw } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import EmptyState from "$lib/EmptyState.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Status from "$lib/Status.svelte";
  import Deadline from "$lib/Deadline.svelte";
  import { session, loadError, api, refresh } from "$lib/api";
  let error = $state("");
  let pending = $state("");
  let checked = $state("");
  async function checkAgain() {
    await refresh();
    if ($loadError) {
      error = $loadError;
      checked = "";
      return;
    }
    error = "";
    checked = new Date().toLocaleTimeString([], {
      hour: "numeric",
      minute: "2-digit",
      second: "2-digit",
    });
  }
  let confirmation: HTMLDialogElement;
  let change = $state<{
    teamID: string;
    name: string;
    oldID: string;
    oldName: string;
  } | null>(null);
  function choose(teamID: string) {
    const current = $session?.teams.find(
      (t) => t.id === $session?.membership.teamID,
    );
    if (!current) {
      void join(teamID);
      return;
    }
    error = "";
    change = {
      teamID,
      name: $session?.teams.find((t) => t.id === teamID)?.name ?? "no team",
      oldID: current.id,
      oldName: current.name,
    };
    confirmation.showModal();
  }
  async function confirmChange() {
    if (!change || pending) return;
    pending = "confirm";
    try {
      await refresh();
      if ($loadError) {
        error =
          "Unable to verify your current team. Retry when the connection is restored.";
        return;
      }
      if ($session?.membership.teamID !== change.oldID) {
        error =
          "Your team changed while this dialog was open. Cancel and review the team list again.";
        return;
      }
      await join(change.teamID);
    } finally {
      pending = "";
    }
  }
  async function join(teamID: string) {
    pending = teamID || "leave";
    error = "";
    try {
      await api("/api/team", { teamID });
      await refresh();
      confirmation?.close();
      if (teamID) await goto("/lab/");
    } catch (e) {
      error = (e as Error).message;
      await refresh();
    } finally {
      pending = "";
    }
  }
</script>

<svelte:head><title>Choose your team · LabCentral</title></svelte:head>
<div>
  <Guard>
    {#if $session?.meeting && !["ended", "ending"].includes($session.meeting.status)}
      <PageHeader title="Choose your team">
        {#snippet eyebrow()}Meeting · {$session?.meeting?.name}{/snippet}
        <Deadline expires={$session.meeting.expires} />
      </PageHeader>

      <Notice message={error} dismissible />
      {#if $session.membership.switching}<div
          class="alert my-4 text-sm"
          role="status"
        >
          <span class="loading loading-spinner loading-sm"></span>Your previous
          desktop is closing. Retry the same team to finish your change.
        </div>{/if}
      <ul class="rounded-box border border-base-content/9 bg-base-200/70 divide-y divide-base-content/8">
        {#each $session.teams as team}
          {@const mine = team.id === $session.membership.teamID}
          {@const full = team.members >= team.capacity}
          <li class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3.5">
            <div class="min-w-28 flex-1">
              <h2 class="flex items-center gap-2 text-sm font-medium">
                {team.name}
                {#if mine}<span class="badge badge-sm badge-ghost">Your team</span>{/if}
              </h2>
              <p class="mt-0.5 text-xs tabular-nums text-base-content/60">
                {team.members} of {team.capacity} seats{#if $session.user.admin || mine}{" · "}<Status status={team.status} />{/if}
              </p>
            </div>
            {#if mine && !$session.membership.switching}<a
                href="/lab/"
                class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
                >Your lab<ArrowRight size={15} /></a
              >
            {:else}<button
                class="btn btn-sm min-h-11 lg:min-h-8"
                disabled={!!pending || full}
                onclick={() => choose(team.id)}
                >{#if pending === team.id}<span
                    class="loading loading-spinner loading-xs"
                  ></span>{/if}{full
                  ? "Team full"
                  : $session.membership.teamID
                    ? "Switch team"
                    : "Join team"}</button
              >{/if}
          </li>{/each}
      </ul>
      {#if $session.membership.teamID}<div class="mt-3 flex justify-end">
          <button
            class="btn btn-sm min-h-11 text-base-content/70"
            disabled={!!pending}
            onclick={() => choose("")}><LogOut size={15} />Leave team</button
          >
        </div>{/if}
    {:else}
      <h1 class="sr-only">Meeting</h1>
      <EmptyState
        icon={Users}
        title={$session?.meeting?.status === "ending"
          ? "This meeting is ending"
          : $session?.meeting?.status === "ended"
            ? "This meeting has ended"
            : "No meeting right now"}
      >
        {$session?.meeting?.status === "ending"
          ? "Team labs are being removed."
          : $session?.meeting?.status === "ended"
            ? "Join the next meeting for a fresh workspace."
            : "Teams appear here when a meeting starts."}
        <Notice message={error} dismissible />
        {#snippet actions()}<a class="btn btn-sm btn-primary min-h-11" href="/labs/"
            >Practice<ArrowRight size={15} /></a
          ><button
            class="btn btn-sm min-h-11"
            onclick={checkAgain}><RotateCw size={15} />Check again</button
          >{#if checked}<p
              class="basis-full text-xs text-base-content/60"
              role="status"
            >
              Checked at {checked}
            </p>{/if}{/snippet}
      </EmptyState>
    {/if}
  </Guard>
</div>
<dialog
  bind:this={confirmation}
  class="modal"
  aria-labelledby="team-change-title"
  oncancel={(e) => {
    if (pending) e.preventDefault();
  }}
>
  <div class="modal-box">
    <h2 id="team-change-title" class="text-lg font-semibold">
      {change?.teamID
        ? `Switch to ${change.name}?`
        : `Leave ${change?.oldName ?? "your team"}?`}
    </h2>
    <p class="mt-3 text-sm leading-relaxed text-base-content/80">
      {#if change?.teamID}From <strong>{change?.oldName}</strong> to
        <strong>{change?.name}</strong>.{/if}
      Your desktop closes. Files stay with {change?.oldName} until the meeting ends.
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={!!pending}>Cancel</button>
      </form>
      <button
        class="btn btn-sm min-h-11 btn-primary"
        disabled={!!pending}
        onclick={confirmChange}
        >{#if pending}<span class="loading loading-spinner loading-xs"
          ></span>{/if}{change?.teamID
          ? `Switch to ${change.name}`
          : `Leave ${change?.oldName ?? "team"}`}</button
      >
    </div>
  </div>
</dialog>
