<script lang="ts">
  import { goto } from "$app/navigation";
  import { Users, ArrowRight, Check, LogOut, RotateCw } from "@lucide/svelte";
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
<div class="max-w-3xl">
  <PageHeader
    title="Choose your team"
    description="Teams share one meeting lab. Each member gets a separate desktop workspace."
  />
  <Guard>
    {#if $session?.meeting && !["ended", "ending"].includes($session.meeting.status)}
      <h2 class="mb-3 text-sm font-medium">{$session.meeting.name}</h2>
      <Deadline expires={$session.meeting.expires} />
      {#if $session.membership.teamID}<p class="mt-4 text-sm leading-relaxed">
          Switching closes your desktop. Files stay with the previous team until
          the meeting ends.
        </p>{/if}
      <Notice message={error} dismissible />
      {#if $session.membership.switching}<div
          class="alert my-4 text-sm"
          role="status"
        >
          <span class="loading loading-spinner loading-sm"></span>Your previous
          desktop is closing. Retry the same team to finish your change.
        </div>{/if}
      <ul
        class="mt-5 divide-y divide-base-content/10 border-y border-base-content/10"
      >
        {#each $session.teams as team}<li
            class="flex flex-wrap items-center gap-3 py-4"
            class:bg-base-200={$session.membership.teamID === team.id}
          >
            <Users
              size={17}
              class="ml-3 shrink-0 text-base-content/75"
              aria-hidden="true"
            />
            <div class="min-w-28 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h3 class="text-sm font-medium">{team.name}</h3>
                {#if team.id === $session.membership.teamID}<span
                    class="inline-flex items-center gap-1 text-xs text-primary"
                    ><Check size={12} />Your team</span
                  >{/if}
              </div>
              <p class="mt-1 text-xs tabular-nums text-base-content/75">
                {team.members} of {team.capacity} seats filled
              </p>
            </div>
            <div class="mr-3 flex flex-wrap items-center gap-3">
              {#if $session.user.admin || team.id === $session.membership.teamID}<Status
                  status={team.status}
                />{/if}
              {#if team.id === $session.membership.teamID && !$session.membership.switching}<a
                  href="/lab/"
                  class="btn btn-sm min-h-11 btn-primary"
                  >Your lab<ArrowRight size={15} /></a
                >
              {:else}<button
                  class="btn btn-sm min-h-11"
                  disabled={!!pending || team.members >= team.capacity}
                  onclick={() => choose(team.id)}
                  >{#if pending === team.id}<span
                      class="loading loading-spinner loading-xs"
                    ></span>{/if}{team.members >= team.capacity
                    ? "Team full"
                    : $session.membership.teamID
                      ? "Switch team"
                      : "Join team"}<ArrowRight size={15} /></button
                >{/if}
            </div>
          </li>{/each}
      </ul>
      {#if $session.membership.teamID}<div class="mt-4 flex justify-end">
          <button
            class="btn btn-sm min-h-11 btn-ghost"
            disabled={!!pending}
            onclick={() => choose("")}><LogOut size={15} />Leave team</button
          >
        </div>{/if}
    {:else}
      <EmptyState
        icon={Users}
        title={$session?.meeting?.status === "ending"
          ? "This meeting is ending"
          : $session?.meeting?.status === "ended"
            ? "This meeting has ended"
            : "No meeting is open yet"}
      >
        {$session?.meeting?.status === "ending"
          ? "Lab cleanup is in progress. Teams are no longer available."
          : $session?.meeting?.status === "ended"
            ? "Lab access has ended. Join the next meeting for a fresh workspace."
            : "Teams appear when a meeting opens."}
        Practice labs stay available in the meantime.
        <Notice message={error} dismissible />
        {#snippet actions()}<button
            class="btn btn-sm min-h-11"
            onclick={checkAgain}><RotateCw size={15} />Check again</button
          ><a class="btn btn-sm btn-ghost min-h-11" href="/labs/"
            >Practice labs<ArrowRight size={15} /></a
          >{#if checked}<p
              class="basis-full text-xs text-base-content/75"
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
    <p class="mt-3 text-sm">
      From <strong>{change?.oldName}</strong> to
      <strong>{change?.name}</strong>.
    </p>
    <p class="mt-3 text-sm leading-relaxed">
      This closes your desktop. Files stay with {change?.oldName} until the meeting
      ends.{change?.teamID
        ? " Your workspace on the new team will be separate."
        : " Join a team again to access a desktop."}
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
