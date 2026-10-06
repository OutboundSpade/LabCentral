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
  import MarkdownIt from "markdown-it";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import Notice from "$lib/Notice.svelte";
  import Status from "$lib/Status.svelte";
  import { session, api, refresh, date } from "$lib/api";
  import type { Launch } from "$lib/types";
  let error = $state("");
  let busy = $state(false);
  const markdown = new MarkdownIt({ html: false, linkify: false });
  let handover = $derived(markdown.render($session?.lab.handover ?? ""));
  async function open() {
    // Reserve the tab during the click; provisioning can outlast browser user activation.
    const tab = window.open("about:blank", "_blank");
    if (!tab) {
      error = "Allow a new tab for LabCentral, then open your desktop again.";
      return;
    }
    tab.opener = null;
    tab.document.title = "Starting your desktop · LabCentral";
    tab.document.body.textContent =
      "Starting your desktop. This can take a minute…";
    busy = true;
    error = "";
    try {
      const result = await api<Launch>("/api/access", {});
      if (tab.closed)
        throw new Error(
          "Desktop tab closed. Open it again when you are ready.",
        );
      const form = tab.document.createElement("form");
      form.method = "POST";
      form.action = result.url;
      const input = tab.document.createElement("input");
      input.type = "hidden";
      input.name = "ticket";
      input.value = result.ticket;
      form.append(input);
      tab.document.body.replaceChildren(form);
      form.submit();
    } catch (e) {
      tab.close();
      error = (e as Error).message;
    } finally {
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
      busy = false;
    }
  }
</script>

<svelte:head><title>Your lab · LabCentral</title></svelte:head>
<PageHeader title="Your lab" />
<Guard
  ><Notice message={error} />
  {#if $session?.meeting?.status === "ended" || $session?.meeting?.status === "ending"}
    <section class="max-w-lg border-t border-base-content/10 py-8">
      <Clock size={24} />
      <h2 class="mt-4 text-lg font-semibold">This meeting has ended</h2>
      <p class="mt-2 text-sm leading-relaxed text-base-content/75">
        The team's machines and workspaces are being removed. Join the next
        meeting for a fresh lab.
      </p>
      <a href="/teams/" class="btn btn-sm min-h-11 mt-5">Back to teams</a>
    </section>
  {:else if !$session?.lab.team}
    <section class="max-w-lg border-t border-base-content/10 py-8">
      <Users size={24} />
      <h2 class="mt-4 text-lg font-semibold">Choose a team</h2>
      <p class="mt-2 text-sm text-base-content/75">
        Join a team to access its lab.
      </p>
      <a href="/teams/" class="btn btn-sm min-h-11 btn-primary mt-5"
        >Choose a team</a
      >
    </section>
  {:else}
    <div class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <aside
        class="min-w-0 lg:order-2 lg:sticky lg:top-6 lg:border-l lg:border-base-content/10 lg:pl-6"
      >
        <div class="flex items-center justify-between gap-3">
          <h2 class="text-base font-semibold">{$session.lab.team.name}</h2>
          <a
            href="/teams/"
            class="link link-hover inline-flex min-h-11 items-center text-xs"
            >Change team</a
          >
        </div>
        <p class="flex items-center gap-2 text-xs text-base-content/75">
          <Clock size={14} />Ends at {$session.meeting
            ? date($session.meeting.expires)
            : "—"}
        </p>
        <section
          class="mt-5 border-y border-base-content/10 py-5"
          aria-label="Your desktop"
        >
          <div class="mb-3 flex items-center justify-between gap-3">
            <h3 class="flex items-center gap-2 text-sm font-medium">
              <Monitor size={17} />Your desktop
            </h3>
            <Status status={$session.lab.team.status} />
          </div>
          <button
            class="btn btn-sm min-h-11 btn-primary w-full"
            disabled={busy || $session.lab.team.status !== "ready"}
            onclick={open}
            >{#if busy}<span class="loading loading-spinner loading-xs"
              ></span>{:else}<ExternalLink size={15} />{/if}{busy
              ? "Starting…"
              : $session.lab.team.status === "ready"
                ? "Open desktop"
                : $session.lab.team.status === "failed"
                  ? "Lab needs attention"
                  : "Preparing your lab"}</button
          >
          <p class="mt-3 text-xs leading-relaxed text-base-content/75">
            Opens in a new tab. Your files stay until the meeting ends.
          </p>
          <button
            class="btn btn-sm min-h-11 btn-ghost mt-2 w-full"
            disabled={busy || $session.lab.team.status !== "ready"}
            onclick={close}><Unplug size={15} />Disconnect desktop</button
          >
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
            </li>{:else}<li class="py-3 text-sm text-base-content/75">
              Preparing machines.
            </li>{/each}
        </ul>
        <h3 class="mt-5 text-sm font-medium">Your teammates</h3>
        <ul class="mt-2 space-y-1 text-sm text-base-content/75">
          {#each $session.lab.teammates ?? [] as member}<li>
              {member.name}
            </li>{:else}<li>Your team roster will appear here.</li>{/each}
        </ul>
        {#if $session.lab.team.status === "failed"}<Notice
            message="Your lab needs administrator attention. Your team membership is saved."
          />{/if}
      </aside>
      <section class="min-w-0 lg:order-1">
        <div
          class="mb-5 flex items-center gap-2 border-b border-base-content/10 pb-4"
        >
          <BookOpen size={17} class="text-base-content/75" />
          <h2 class="text-base font-semibold">Student handover</h2>
        </div>
        <article
          class="prose prose-invert prose-sm max-w-[68ch] prose-headings:font-semibold prose-headings:tracking-tight prose-h1:text-xl prose-h2:text-base prose-h3:text-sm prose-a:text-primary"
        >
          {@html handover}
        </article>
      </section>
    </div>
  {/if}</Guard
>
