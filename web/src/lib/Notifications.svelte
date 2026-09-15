<script lang="ts">
  import { X, CircleAlert, CircleCheck } from "@lucide/svelte";
  import { afterNavigate } from "$app/navigation";
  let { error = $bindable(""), notice = $bindable("") } = $props();
  let hovered = $state(false);
  let focused = $state(false);
  const paused = $derived(hovered || focused);
  $effect(() => {
    if (!error && !notice) {
      hovered = false;
      focused = false;
    }
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
    if (from) {
      error = "";
      notice = "";
    }
  });
</script>

{#if error || notice}
  <section
    aria-label="Notifications"
    class="fixed right-4 bottom-4 z-50 grid w-[min(26rem,calc(100vw-2rem))] gap-3"
    onpointerenter={() => (hovered = true)}
    onpointerleave={() => (hovered = false)}
    onfocusin={() => (focused = true)}
    onfocusout={() => (focused = false)}
  >
    {#if error}<div
        role="alert"
        class="flex items-start gap-3 rounded-xl border border-base-300 bg-base-100 p-4 text-sm shadow-xl"
      >
        <CircleAlert size={20} class="mt-0.5 shrink-0 text-error" /><span
          class="min-w-0 flex-1 break-words leading-6">{error}</span
        >
        <button
          class="btn btn-sm btn-ghost btn-square"
          aria-label="Dismiss error"
          onclick={() => (error = "")}><X size={16} /></button
        >
      </div>{/if}
    {#if notice}<div
        role="status"
        class="flex items-start gap-3 rounded-xl border border-base-300 bg-base-100 p-4 text-sm shadow-xl"
      >
        <CircleCheck size={20} class="mt-0.5 shrink-0 text-success" /><span
          class="min-w-0 flex-1 break-words leading-6">{notice}</span
        >
        <button
          class="btn btn-sm btn-ghost btn-square"
          aria-label="Dismiss notification"
          onclick={() => (notice = "")}><X size={16} /></button
        >
      </div>{/if}
  </section>
{/if}
