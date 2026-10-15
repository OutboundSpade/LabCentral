<script lang="ts">
  import { CircleAlert, X } from "@lucide/svelte";
  let {
    message,
    dismissible = false,
  }: { message: string; dismissible?: boolean } = $props();
  let dismissed = $state("");
  $effect(() => {
    if (!message) dismissed = "";
  });
</script>

{#if message && message !== dismissed}<div
    role="alert"
    class="alert alert-soft alert-error my-5"
  >
    <CircleAlert size={18} aria-hidden="true" /><span
      class="min-w-0 break-words">{message}</span
    >
    {#if dismissible}<button
        type="button"
        class="btn btn-sm min-h-11 min-w-11"
        aria-label="Dismiss error"
        onclick={() => (dismissed = message)}><X size={16} /></button
      >{/if}
  </div>{/if}
