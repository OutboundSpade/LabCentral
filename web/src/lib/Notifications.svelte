<script lang="ts">
  import { X } from "@lucide/svelte";
  import { afterNavigate } from "$app/navigation";
  let { error = $bindable(""), notice = $bindable("") } = $props();
  let hovered = $state(false);
  let focused = $state(false);
  const paused = $derived(hovered || focused);
  $effect(() => {
    if (!error && !notice) { hovered = false; focused = false; }
  });
  $effect(() => {
    if (!notice || paused) return;
    const timer = setTimeout(() => (notice = ""), 5000);
    return () => clearTimeout(timer);
  });
  $effect(() => {
    if (!error || paused) return;
    const timer = setTimeout(() => (error = ""), 8000);
    return () => clearTimeout(timer);
  });
  afterNavigate(({ from }) => {
    if (from) { error = ""; notice = ""; }
  });
</script>

{#if error || notice}
  <section
    aria-label="Notifications"
    class="fixed right-4 top-4 z-50 grid w-[min(26rem,calc(100vw-2rem))] gap-3"
    onpointerenter={() => (hovered = true)}
    onpointerleave={() => (hovered = false)}
    onfocusin={() => (focused = true)}
    onfocusout={() => (focused = false)}
  >
    {#if error}<div role="alert" class="alert alert-error shadow-lg">
      <span class="min-w-0 break-words">{error}</span>
      <button class="btn btn-sm btn-ghost btn-square" aria-label="Dismiss error" onclick={() => (error = "")}><X size={16} /></button>
    </div>{/if}
    {#if notice}<div role="status" class="alert alert-success shadow-lg">
      <span class="min-w-0 break-words">{notice}</span>
      <button class="btn btn-sm btn-ghost btn-square" aria-label="Dismiss notification" onclick={() => (notice = "")}><X size={16} /></button>
    </div>{/if}
  </section>
{/if}
