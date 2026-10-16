<script lang="ts">
  import {
    ChevronRight,
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
  import { session, api, refresh, APIError } from "$lib/api";
  import { launchDesktop } from "$lib/launch";
  import { render, prose } from "$lib/markdown";
  let error = $state("");
  let busy = $state(false);
  let handover = $derived(render($session?.lab.handover ?? ""));
  let readyNotice = $state(false);
  let confirmTakeover = $state(false);
  // A shared console has one screen: one teammate holds it at a time.
  const desktop = $derived($session?.lab.desktopState ?? null);
  const windows = $derived(
    ($session?.lab.machines ?? []).some((m) => m.desktop && m.os === "windows"),
  );
  const holder = $derived(
    desktop?.code === "held"
      ? (desktop.holderName ?? "A teammate")
      : desktop?.code === "desktop_taken_over"
        ? (desktop.takenOverBy ?? "A teammate")
        : "",
  );
  const handingOver = $derived(desktop?.code === "desktop_outcome_unknown");
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
  async function open(takeover = false) {
    readyNotice = false;
    confirmTakeover = false;
    busy = true;
    error = "";
    try {
      await launchDesktop(
        takeover ? "/api/access/takeover" : "/api/access",
        windows
          ? "Starting the desktop. This can take a few minutes…"
          : "Starting your desktop. This can take a minute…",
      );
    } catch (e) {
      // Someone else took the console meanwhile; the refreshed state offers take over.
      if (!(e instanceof APIError && e.status === 409 && e.code.startsWith("desktop_")))
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
          >Practice</a
        ><a href="/teams/" class="btn btn-sm min-h-11">Back to teams</a
        >{/snippet}
    </EmptyState>
  {:else if !$session?.lab.team}
    <h1 class="sr-only">Your lab</h1>
    <EmptyState icon={Users} title="Choose a team">
      Join a team to get your meeting desktop.
      {#snippet actions()}<a href="/teams/" class="btn btn-sm min-h-11 btn-primary"
          >Choose a team</a
        ><a href="/labs/" class="btn btn-sm min-h-11"
          >Practice</a
        >{/snippet}
    </EmptyState>
  {:else}
    <PageHeader title={$session.lab.team.name}>
      {#snippet eyebrow()}{$session?.meeting?.name ?? "Your lab"}{/snippet}
      <a href="/teams/" class="btn btn-sm min-h-11 lg:min-h-8 text-base-content/70"
        >Switch team</a
      >
    </PageHeader>
    <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_19rem] lg:gap-12">
      <aside class="min-w-0 space-y-4 lg:sticky lg:top-20 lg:order-2">
        <section class="rounded-box border border-base-content/9 bg-base-200/70 p-5" aria-labelledby="desktop-title">
          <div class="flex items-center justify-between gap-3">
            <h2 id="desktop-title" class="text-sm font-medium">Desktop</h2>
            <span aria-live="polite" aria-atomic="true"
              ><Status status={$session.lab.team.status} /></span
            >
          </div>
          {#if $session.meeting}<div class="mt-2"><Deadline
              expires={$session.meeting.expires}
            /></div>{/if}
          {#if holder && $session.lab.team.status === "ready"}<p class="mt-4 text-sm leading-relaxed" role="status">
              {desktop?.code === "held"
                ? `${holder} is using the desktop.`
                : `${holder} took over the desktop.`}
              <span class="text-base-content/70">It has one screen, so one teammate uses it at a time.</span>
            </p>
            {#if confirmTakeover}<div class="mt-3 rounded-box border border-warning/30 bg-warning/8 p-3">
                <p class="text-xs leading-relaxed">
                  {holder} will be disconnected. Saved files stay; unsaved work in open programs is lost.
                </p>
                <div class="mt-3 flex flex-wrap gap-2">
                  <button class="btn btn-sm btn-warning min-h-11 lg:min-h-8" disabled={busy} onclick={() => open(true)}
                    ><ExternalLink size={15} />Take over</button
                  ><button class="btn btn-sm btn-ghost min-h-11 lg:min-h-8" onclick={() => (confirmTakeover = false)}
                    >Cancel</button
                  >
                </div>
              </div>{:else}<button
                class="btn mt-3 min-h-11 w-full"
                disabled={busy || !desktop?.canTakeOver}
                aria-busy={busy}
                onclick={() => (confirmTakeover = true)}
                >{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}{desktop?.code === "held"
                  ? "Take over…"
                  : "Take back…"}</button
              >{/if}
          {:else}<button
            class={[
              "btn btn-primary mt-4 min-h-11 w-full",
              readyNotice
                ? "ring-2 ring-primary ring-offset-2 ring-offset-base-100"
                : "",
            ]}
            disabled={busy || handingOver || $session.lab.team.status !== "ready"}
            aria-busy={busy}
            aria-describedby="desktop-help"
            onclick={() => open()}
            >{#if busy}<span class="loading loading-spinner loading-xs"
              ></span>{:else}<ExternalLink size={15} />{/if}Open desktop</button
          >
          <p id="desktop-help" class="mt-2.5 text-xs leading-relaxed text-base-content/70">
            {busy
              ? windows
                ? "Starting the desktop…"
                : "Starting your desktop…"
              : handingOver
                ? "The desktop is being handed over. Try again in a moment."
                : $session.lab.team.status === "failed"
                  ? "Your lab needs administrator attention. Your team membership is saved."
                  : $session.lab.team.status !== "ready"
                    ? "Your lab is being prepared."
                    : readyNotice
                      ? "Your lab is ready."
                      : desktop
                        ? "Opens in a new tab. Your team shares this desktop."
                        : "Opens in a new tab."}
          </p>{/if}
          {#if $session.lab.desktopActive}<div class="mt-4 flex items-center justify-between gap-2 border-t border-base-content/8 pt-3">
            <p class="flex items-center gap-2 text-xs whitespace-nowrap text-base-content/70" role="status">
              <span
                class={["size-1.5 rounded-full", $session.lab.desktopActive ? "bg-success" : "bg-base-content/30"]}
                aria-hidden="true"
              ></span>{$session.lab.desktopActive
                ? "Desktop open"
                : "Desktop closed"}
            </p>
            <button
              class="btn btn-sm min-h-11 lg:min-h-8 text-base-content/70"
              disabled={busy || !$session.lab.desktopActive}
              title="Closes your session. Files remain until the meeting ends."
              onclick={close}><Unplug size={13} />Disconnect desktop</button
            >
          </div>{/if}
          <details class="group mt-3 text-sm">
            <summary class="flex min-h-11 cursor-pointer list-none items-center gap-1 text-xs text-base-content/70 hover:text-base-content lg:min-h-8 [&::-webkit-details-marker]:hidden"
              ><ChevronRight size={14} aria-hidden="true" class="transition-transform group-open:rotate-90" />Using your desktop</summary
            >
            <ul class="mt-2 [list-style-type:disc] space-y-1.5 pl-4 text-xs leading-relaxed text-base-content/80">
              {#if !desktop || windows}<li>After 60 idle minutes the desktop disconnects; reopen it to resume.</li>{/if}
              {#if desktop}<li>Your team shares one screen. Taking over disconnects whoever is using it; saved files stay.</li>{/if}
              {#if desktop && !windows}<li>Use Console login for the guest greeter. Console clipboard depends on guest support.</li>{/if}
              {#if windows}<li>Paste inside the desktop with Ctrl+V (⌘V).</li>
              {:else}<li>Paste inside the desktop with Ctrl+V (⌘V); terminals use Ctrl+Shift+V.</li>{/if}
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
                >{#if machine.os === "windows"}<span class="badge badge-ghost badge-sm">Windows</span>{/if}<Status
                  status={machine.status}
                />
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
