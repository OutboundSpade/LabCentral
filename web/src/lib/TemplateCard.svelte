<script lang="ts">
  import type { Snippet } from "svelte";
  import { Clock, Gauge } from "@lucide/svelte";
  import { templateName } from "./display";
  import {
    difficultyLabel,
    minutes,
    scenarios,
    type LibraryTemplate,
  } from "./library";
  // One card for a template in the practice library and in administration.
  // The whole card opens the template (its title link covers the card); the page
  // supplies the status (left) and any other actions (right) for its audience.
  let {
    template: t,
    href,
    badge,
    status,
    actions,
  }: {
    template: LibraryTemplate;
    href: string;
    badge?: Snippet;
    status?: Snippet;
    actions?: Snippet;
  } = $props();
  const meta = $derived(t.library);
  const count = $derived(scenarios(t).length);
  const windows = $derived(t.machines.some((m) => m.os === "windows"));
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
</script>

<li
  class="card card-sm relative min-w-0 border border-base-content/9 bg-base-200/70 transition-colors hover:border-base-content/20 hover:bg-base-200"
>
  <div class="card-body gap-3">
    <div class="flex items-start justify-between gap-3">
      <h2 class="card-title break-words text-base leading-snug">
        <a
          class="after:absolute after:inset-0 after:rounded-box focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-offset-2 focus-visible:after:outline-primary"
          {href}>{templateName(t.name)}</a
        >
      </h2>
      {@render badge?.()}
    </div>
    <p class="line-clamp-2 min-h-[2lh] grow-0 text-sm leading-relaxed text-base-content/70">
      {t.description || "No description."}
    </p>
    {#if t.coachOnly || windows || meta?.difficulty || meta?.estimatedMinutes || meta?.category}<div
        class="flex flex-wrap gap-1.5"
      >
      {#if t.coachOnly}<span class="badge badge-sm badge-ghost">Coach-only</span>{/if}
      {#if windows}<span class="badge badge-sm badge-ghost">Windows</span>{/if}
      {#if meta?.difficulty}<span class="badge badge-sm badge-ghost"
          ><Gauge size={12} aria-hidden="true" />{difficultyLabel(meta.difficulty)}</span
        >{/if}
      {#if meta?.estimatedMinutes}<span class="badge badge-sm badge-ghost"
          ><Clock size={12} aria-hidden="true" />{minutes(meta.estimatedMinutes)}</span
        >{/if}
      {#if meta?.category}<span class="badge badge-sm badge-ghost">{meta.category}</span>{/if}
    </div>{/if}
    <p class="grow-0 text-xs tabular-nums text-base-content/60">
      {plural(t.machines.length, "machine")} · {plural(t.stageCount, "stage")}{#if count}{" · "}{plural(count, "scenario")}{/if}
    </p>
    <div
      class="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-base-content/8 pt-3"
    >
      <div class="min-w-0 flex-1">{@render status?.()}</div>
      {#if actions}<div class="relative flex flex-wrap justify-end gap-2">{@render actions()}</div>{/if}
    </div>
  </div>
</li>
