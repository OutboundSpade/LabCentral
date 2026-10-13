<script lang="ts">
  import {
    Server,
    Clock,
    ExternalLink,
    Users,
    Unplug,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Status from "$lib/Status.svelte";
  import Deadline from "$lib/Deadline.svelte";
  import EmptyState from "$lib/EmptyState.svelte";
  import { session, api, refresh } from "$lib/api";
  import { launchDesktop } from "$lib/launch";
  import { render, prose } from "$lib/markdown";
  let error = $state("");
  let busy = $state(false);
  let handover = $derived(render($session?.lab.handover ?? ""));
  let readyNotice = $state(false);
  let observedTeam = $state("");
  let previousStatus = $state("");
  $effect(() => {
    const team = $session?.lab.team;
    if (observedTeam !== (team?.id ?? "")) {
      observedTeam = team?.id ?? "";
      previousStatus = team?.status ?? "";
      readyNotice = false;
      return;
    }
    if (
      previousStatus &&
      previousStatus !== "ready" &&
      team?.status === "ready"
    )
      readyNotice = true;
    if (team?.status !== "ready") readyNotice = false;
    previousStatus = team?.status ?? "";
  });
  async function open() {
    readyNotice = false;
    busy = true;
    error = "";
    try {
      await launchDesktop(
        "/api/access",
        "Starting your desktop. This can take a minute…",
      );
    } catch (e) {
      error = (e as Error).message;
    } finally {
      await refresh();
      busy = false;
    }
  }
  async function close() {
    busy = true;
    error = "";
    try {
      await api("/api/access/close", {});
    } catch (e) {
      error = (e as Error).message;
    } finally {
      await refresh();
      busy = false;
    }
  }
</script>

<svelte:head><title>{readyNotice ? "Lab ready" : "Your lab"} · LabCentral</title></svelte:head>
<Guard
  ><Notice message={error} dismissible />
  {#if $session?.meeting?.status === "ended" || $session?.meeting?.status === "ending"}
    <h1 class="sr-only">Your lab</h1>
    <EmptyState
      icon={Clock}
      title={$session.meeting.status === "ending"
        ? "This meeting is ending"
        : "This meeting has ended"}
    >
      {$session.meeting.status === "ending"
        ? "Team machines and workspaces are being removed."
        : "Workspace files are no longer available."}
      {#snippet actions()}<a href="/labs/" class="btn btn-sm min-h-11 btn-primary"
          >Practice labs</a
        ><a href="/teams/" class="btn btn-sm btn-ghost min-h-11">Back to teams</a
        >{/snippet}
    </EmptyState>
  {:else if !$session?.lab.team}
    <h1 class="sr-only">Your lab</h1>
    <EmptyState icon={Users} title="Choose a team">
      Join a team to get your meeting desktop.
      {#snippet actions()}<a href="/teams/" class="btn btn-sm min-h-11 btn-primary"
          >Choose a team</a
        ><a href="/labs/" class="btn btn-sm min-h-11 btn-ghost"
          >Practice labs</a
        >{/snippet}
    </EmptyState>
  {:else}
    <PageHeader title={$session.lab.team.name}>
      {#snippet eyebrow()}{$session?.meeting?.name ?? "Your lab"}{/snippet}
      <a href="/teams/" class="btn btn-sm btn-ghost min-h-11 lg:min-h-8 text-base-content/70"
        >Change team</a
      >
    </PageHeader>
    <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_19rem] lg:gap-12">
      <aside class="min-w-0 space-y-4 lg:sticky lg:top-20 lg:order-2">
        <section class="rounded-box border border-base-content/9 bg-base-200/70 p-5" aria-labelledby="desktop-title">
          <div class="flex items-center justify-between gap-3">
            <h2 id="desktop-title" class="text-sm font-medium">Your desktop</h2>
            <span aria-live="polite" aria-atomic="true"
              ><Status status={$session.lab.team.status} /></span
            >
          </div>
          {#if $session.meeting}<div class="mt-2"><Deadline
              expires={$session.meeting.expires}
            /></div>{/if}
          <button
            class={[
              "btn btn-primary mt-4 min-h-11 w-full",
              readyNotice
                ? "ring-2 ring-primary ring-offset-2 ring-offset-base-100"
                : "",
            ]}
            disabled={busy || $session.lab.team.status !== "ready"}
            aria-busy={busy}
            aria-describedby="desktop-help"
            onclick={open}
            >{#if busy}<span class="loading loading-spinner loading-xs"
              ></span>{:else}<ExternalLink size={15} />{/if}Open desktop</button
          >
          <p id="desktop-help" class="mt-2.5 text-xs leading-relaxed text-base-content/70">
            {busy
              ? "Starting your desktop…"
              : $session.lab.team.status === "failed"
                ? "Your lab needs administrator attention. Your team membership is saved."
                : $session.lab.team.status !== "ready"
                  ? "Your lab is being prepared."
                  : readyNotice
                    ? "Your lab is ready."
                    : "Opens in a new tab and resumes where you left off."}
          </p>
          <div class="mt-4 flex items-center justify-between gap-2 border-t border-base-content/8 pt-3">
            <p class="flex items-center gap-2 text-xs whitespace-nowrap text-base-content/70" role="status">
              <span
                class={["size-1.5 rounded-full", $session.lab.desktopActive ? "bg-success" : "bg-base-content/30"]}
                aria-hidden="true"
              ></span>{$session.lab.desktopActive
                ? "Session open"
                : "Not connected"}
            </p>
            <button
              class="btn btn-xs btn-ghost min-h-9 text-base-content/70"
              disabled={busy || !$session.lab.desktopActive}
              title="Closes your session. Files remain until the meeting ends."
              onclick={close}><Unplug size={13} />Disconnect desktop</button
            >
          </div>
          <details class="mt-3 text-sm">
            <summary class="cursor-pointer text-xs text-base-content/70 hover:text-base-content"
              >Using your desktop</summary
            >
            <ul class="mt-2 [list-style-type:disc] space-y-1.5 pl-4 text-xs leading-relaxed text-base-content/80">
              <li>After 60 idle minutes the desktop disconnects; reopen it to resume.</li>
              <li>Paste inside the desktop with Ctrl+V (⌘V); terminals use Ctrl+Shift+V.</li>
              <li><strong>Copy from desktop</strong> needs clipboard permission.</li>
              <li>In Firefox or Safari, use <strong>Paste to desktop</strong> for text.</li>
            </ul>
          </details>
        </section>
        <section class="rounded-box border border-base-content/9 bg-base-200/70 p-5" aria-labelledby="team-title">
          <h2 id="team-title" class="text-xs font-medium text-base-content/70">Machines</h2>
          <ul class="mt-2 space-y-1.5">
            {#each $session.lab.machines as machine}<li
                class="flex items-center gap-2.5 text-sm"
              >
                <Server size={14} class="text-base-content/50" aria-hidden="true" /><span
                  class="flex-1 truncate">{machine.name}</span
                ><Status status={machine.status} />
              </li>{:else}<li class="text-sm text-base-content/70">Preparing machines…</li>{/each}
          </ul>
          <h2 class="text-xs font-medium text-base-content/70 mt-5">Teammates</h2>
          <p class="mt-2 text-sm leading-relaxed">
            {($session.lab.teammates ?? []).map((m) => m.name).join(", ") || "No one else yet."}
          </p>
        </section>
      </aside>
      <section class="min-w-0 lg:order-1" aria-label="Handover">
        <article class={prose}>
          {@html handover}
        </article>
      </section>
    </div>
  {/if}
</Guard>
