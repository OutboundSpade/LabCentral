<script lang="ts">
  import { Clock, TriangleAlert } from "@lucide/svelte";
  import { date } from "./api";
  import { now } from "./time";
  let {
    expires,
    label = "Ends at",
    warning = "Your files will be deleted when the meeting ends. Save your work now.",
    due = "The meeting deadline has passed. Lab cleanup is due.",
  }: { expires: string; label?: string; warning?: string; due?: string } =
    $props();
  const remaining = $derived(Math.max(0, Date.parse(expires) - $now));
  const minutes = $derived(Math.ceil(remaining / 60000));
  const urgent = $derived(remaining <= 15 * 60000);
</script>

<div class="text-sm">
  <p class="flex flex-wrap items-center gap-x-3 gap-y-1 tabular-nums">
    <span class="inline-flex items-center gap-1.5"
      ><Clock size={14} />{label}
      <time datetime={expires}>{date(expires)}</time></span
    >
    <span class="font-medium" class:text-warning={urgent}
      >{remaining === 0
        ? "Cleanup due now"
        : minutes >= 60
          ? `${Math.floor(minutes / 60)} h ${minutes % 60} m left`
          : `${minutes} m left`}</span
    >
  </p>
  {#if urgent}
    <div class="alert alert-soft alert-warning mt-3 text-sm" role="alert">
      <TriangleAlert size={18} aria-hidden="true" /><span
        >{remaining === 0 ? due : "15 minutes or less remain."}
        {warning}</span
      >
    </div>
  {/if}
</div>
