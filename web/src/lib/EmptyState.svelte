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

<section class="panel mx-auto max-w-md px-6 py-10 text-center">
  <span
    class="mx-auto grid size-11 place-items-center rounded-full bg-base-300 text-base-content/70"
    ><Icon size={20} /></span
  >
  <h2 class="mt-4 text-lg font-semibold">{title}</h2>
  {#if children}<div class="mt-2 text-sm leading-relaxed text-base-content/70">
      {@render children()}
    </div>{/if}
  {#if actions}<div class="mt-6 flex flex-wrap items-center justify-center gap-2">
      {@render actions()}
    </div>{/if}
</section>
