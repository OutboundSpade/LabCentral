<script lang="ts">
  import type { Snippet } from "svelte";
  let {
    title,
    description,
    children,
  }: { title: string; description?: string; children?: Snippet } = $props();
</script>

<div class="mb-6 flex flex-wrap items-start justify-between gap-4">
  <div class="min-w-0">
    <h1 class="text-xl font-semibold tracking-tight">{title}</h1>
    {#if description}<p class="mt-1 max-w-[72ch] text-sm leading-relaxed text-base-content/75">
        {description}
      </p>{/if}
  </div>
  {#if children}<div class="flex flex-wrap items-center gap-2">
      {@render children()}
    </div>{/if}
</div>
