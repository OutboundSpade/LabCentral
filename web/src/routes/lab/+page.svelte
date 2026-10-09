<script lang="ts">
  import {
    Monitor,
    Server,
    Clock,
    ExternalLink,
    Users,
    BookOpen,
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

<svelte:head
  ><title>{readyNotice ? "Lab ready" : "Your lab"} · LabCentral</title
  ></svelte:head
>
<PageHeader
  title="Your lab"
  description="Your team's meeting desktop and handover. Meeting labs are removed when the meeting ends."
/>
<Guard
  ><Notice message={error} dismissible />
  {#if $session?.meeting?.status === "ended" || $session?.meeting?.status === "ending"}
    <EmptyState
      icon={Clock}
      title={$session.meeting.status === "ending"
        ? "This meeting is ending"
        : "This meeting has ended"}
    >
      {$session.meeting.status === "ending"
        ? "The team's machines and workspaces are being removed."
        : "The team's lab access has ended. Workspace files are no longer available."}
      Join the next meeting for a fresh lab, or keep practicing from Labs.
      {#snippet actions()}<a href="/labs/" class="btn btn-sm min-h-11 btn-primary"
          >Practice labs</a
        ><a href="/teams/" class="btn btn-sm min-h-11">Back to teams</a
        >{/snippet}
    </EmptyState>
  {:else if !$session?.lab.team}
    <EmptyState icon={Users} title="Choose a team">
      Join a team to access its meeting lab.
      {#snippet actions()}<a href="/teams/" class="btn btn-sm min-h-11 btn-primary"
          >Choose a team</a
        ><a href="/labs/" class="btn btn-sm min-h-11 btn-ghost"
          >Practice labs</a
        >{/snippet}
    </EmptyState>
  {:else}
    <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <aside
        class="min-w-0 lg:order-2 lg:border-l lg:border-base-content/10 lg:pl-6"
      >
        <div class="flex items-center justify-between gap-3">
          <h2 class="text-base font-semibold">{$session.lab.team.name}</h2>
          <a
            href="/teams/"
            class="link link-hover inline-flex min-h-11 items-center text-sm"
            >Change team</a
          >
        </div>
        {#if $session.meeting}<Deadline
            expires={$session.meeting.expires}
          />{/if}
        <section
          class="mt-5 border-y border-base-content/10 py-5"
          aria-label="Your desktop"
        >
          <div class="mb-3 flex items-center justify-between gap-3">
            <h3 class="flex items-center gap-2 text-sm font-medium">
              <Monitor size={17} />Your desktop
            </h3>
            <span aria-live="polite" aria-atomic="true"
              ><Status status={$session.lab.team.status} /></span
            >
          </div>
          <button
            class={[
              "btn btn-sm min-h-11 btn-primary w-full",
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
          <p id="desktop-help" class="mt-3 text-sm leading-relaxed">
            {busy
              ? "Starting your desktop…"
              : $session.lab.team.status === "failed"
                ? "Your lab needs administrator attention before the desktop can open. Your team membership is saved."
                : $session.lab.team.status !== "ready"
                  ? "Your lab is being prepared. The desktop will be available when it is ready."
                  : readyNotice
                    ? "Your lab is ready. You can open your desktop now."
                    : "Opens in a new tab. Reopening resumes your workspace."}
          </p>
          <p class="mt-3 text-sm" role="status">
            {$session.lab.desktopActive
              ? "Desktop session open."
              : "No desktop session open."}
          </p>
          <details
            class="collapse collapse-arrow mt-4 border border-base-content/10"
          >
            <summary class="collapse-title text-sm font-medium"
              >Using your desktop</summary
            >
            <div class="collapse-content text-sm">
              <ul class="space-y-3 leading-relaxed">
                <li>
                  After 60 minutes without input, DCV disconnects. Reopen the
                  desktop to resume your workspace.
                </li>
                <li>
                  Click inside the desktop, then paste with Ctrl+V (⌘V on
                  macOS). Terminals use Ctrl+Shift+V.
                </li>
                <li>
                  <strong>Copy from desktop</strong> needs browser clipboard permission.
                </li>
                <li>
                  In Firefox or Safari, use <strong>Paste to desktop</strong> for
                  text, then paste inside the desktop.
                </li>
              </ul>
            </div>
          </details>
          <div class="mt-4 border-t border-base-content/10 pt-3">
            <button
              class="btn btn-sm min-h-11 btn-ghost w-full"
              disabled={busy || !$session.lab.desktopActive}
              onclick={close}><Unplug size={15} />Disconnect desktop</button
            >
            <p class="mt-1 text-xs text-base-content/75">
              Disconnect closes your session. Files remain until the meeting
              ends.
            </p>
          </div>
        </section>
        <h3 class="mt-5 text-sm font-medium">Your team's machines</h3>
        <ul class="mt-2 divide-y divide-base-content/10">
          {#each $session.lab.machines as machine}<li
              class="flex items-center gap-3 py-2.5"
            >
              <Server
                size={15}
                class="text-base-content/75"
                aria-hidden="true"
              /><span class="flex-1 text-sm">{machine.name}</span><Status
                status={machine.status}
              />
            </li>{:else}<li class="py-3 text-sm">Preparing machines.</li>{/each}
        </ul>
        <h3 class="mt-5 text-sm font-medium">Your teammates</h3>
        <ul class="mt-2 space-y-1 text-sm">
          {#each $session.lab.teammates ?? [] as member}<li>
              {member.name}
            </li>{:else}<li>Your team roster will appear here.</li>{/each}
        </ul>
      </aside>
      <section class="min-w-0 lg:order-1">
        <div
          class="mb-5 flex items-center gap-2 border-b border-base-content/10 pb-4"
        >
          <BookOpen size={17} class="text-base-content/75" />
          <h2 class="text-base font-semibold">Student handover</h2>
        </div>
        <article class={prose}>
          {@html handover}
        </article>
      </section>
    </div>
  {/if}
</Guard>
