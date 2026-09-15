<script lang="ts">
  import { X } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  let {
    open = $bindable(false),
    title,
    children,
    wide = false,
  }: {
    open?: boolean;
    title: string;
    children: Snippet;
    wide?: boolean;
  } = $props();
  let dialog = $state<HTMLDialogElement>();
  const id = $props.id();
  $effect(() => {
    if (open && dialog && !dialog.open) dialog.showModal();
    else if (!open && dialog?.open) dialog.close();
  });
</script>

<dialog
  bind:this={dialog}
  class="modal"
  aria-labelledby={id}
  onclose={() => (open = false)}
>
  <div class:!max-w-4xl={wide} class="modal-box max-h-[90dvh]">
    <header class="mb-6 flex items-center justify-between gap-4">
      <h2 {id} class="text-lg font-semibold">{title}</h2>
      <button
        type="button"
        class="btn btn-sm btn-ghost btn-square"
        aria-label="Close dialog"
        onclick={() => (open = false)}><X size={18} /></button
      >
    </header>
    {@render children()}
  </div>
</dialog>
