<script lang="ts">
  import type { Snippet } from "svelte";
  let {
    title,
    description,
    eyebrow,
    children,
  }: {
    title: string;
    description?: string;
    eyebrow?: Snippet;
    children?: Snippet;
  } = $props();
</script>

<div class="mb-8 flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
  <div class="min-w-0">
    {#if eyebrow}<div class="eyebrow mb-1.5">{@render eyebrow()}</div>{/if}
    <h1 class="text-2xl font-semibold tracking-tight">{title}</h1>
    {#if description}<p class="mt-1.5 max-w-[60ch] text-sm text-base-content/70">
        {description}
      </p>{/if}
  </div>
  {#if children}<div class="flex flex-wrap items-center gap-2">
      {@render children()}
    </div>{/if}
</div>
