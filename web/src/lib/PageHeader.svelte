<script lang="ts">
  import type { Snippet } from "svelte";
  let {
    title,
    description,
    eyebrow,
    children,
    compact = false,
    titleId,
  }: {
    title: string;
    compact?: boolean;
    titleId?: string;
    description?: string;
    eyebrow?: Snippet;
    children?: Snippet;
  } = $props();
</script>

<div class={["flex flex-wrap items-end justify-between gap-x-6", compact ? "gap-y-1.5" : "mb-8 gap-y-4"]}>
  <div class="min-w-0 max-w-full">
    {#if eyebrow}<div class="text-xs font-medium text-base-content/70 mb-1.5">{@render eyebrow()}</div>{/if}
    <h1 id={titleId} class={["break-words font-semibold tracking-tight", compact ? "text-sm" : "text-2xl"]}>{title}</h1>
    {#if description}<p class="mt-1.5 max-w-[60ch] text-sm text-base-content/70">
        {description}
      </p>{/if}
  </div>
  {#if children}<div class="flex flex-wrap items-center gap-2">
      {@render children()}
    </div>{/if}
</div>
