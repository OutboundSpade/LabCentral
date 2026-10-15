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
  // The page supplies the status (left) and actions (right) for its audience.
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
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
</script>

<li
  class="card card-sm min-w-0 border border-base-content/9 bg-base-200/70 transition-colors hover:border-base-content/20"
>
  <div class="card-body gap-3">
    <div class="flex items-start justify-between gap-3">
      <h2 class="card-title break-words text-base leading-snug">
        <a class="hover:underline" {href}>{templateName(t.name)}</a>
      </h2>
      {@render badge?.()}
    </div>
    <p class="line-clamp-2 min-h-[2lh] text-sm leading-relaxed text-base-content/70">
      {t.description || "No description."}
    </p>
    {#if t.coachOnly || meta?.difficulty || meta?.estimatedMinutes || meta?.category}<div
        class="flex flex-wrap gap-1.5"
      >
      {#if t.coachOnly}<span class="badge badge-sm badge-ghost">Coach-only</span>{/if}
      {#if meta?.difficulty}<span class="badge badge-sm badge-ghost"
          ><Gauge size={12} aria-hidden="true" />{difficultyLabel(meta.difficulty)}</span
        >{/if}
      {#if meta?.estimatedMinutes}<span class="badge badge-sm badge-ghost"
          ><Clock size={12} aria-hidden="true" />{minutes(meta.estimatedMinutes)}</span
        >{/if}
      {#if meta?.category}<span class="badge badge-sm badge-ghost">{meta.category}</span>{/if}
    </div>{/if}
    <p class="text-xs tabular-nums text-base-content/60">
      {plural(t.machines.length, "machine")} · {plural(t.stageCount, "stage")}{#if count}{" · "}{plural(count, "scenario")}{/if}
    </p>
    <div
      class="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-base-content/8 pt-3"
    >
      <div class="min-w-0">{@render status?.()}</div>
      <div class="flex flex-wrap justify-end gap-2">{@render actions?.()}</div>
    </div>
  </div>
</li>
