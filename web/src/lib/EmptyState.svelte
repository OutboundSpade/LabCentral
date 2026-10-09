<script lang="ts">
  import type { Component, Snippet } from "svelte";
  let {
    icon: Icon,
    title,
    children,
    actions,
  }: {
    icon: Component<{ size?: number }>;
    title: string;
    children?: Snippet;
    actions?: Snippet;
  } = $props();
</script>

<section class="max-w-lg border-t border-base-content/10 py-8">
  <span class="text-base-content/75"><Icon size={22} /></span>
  <h2 class="mt-4 text-lg font-semibold">{title}</h2>
  {#if children}<div class="mt-2 text-sm leading-relaxed">
      {@render children()}
    </div>{/if}
  {#if actions}<div class="mt-5 flex flex-wrap items-center gap-2">
      {@render actions()}
    </div>{/if}
</section>
