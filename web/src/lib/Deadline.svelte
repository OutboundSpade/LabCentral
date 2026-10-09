<script lang="ts">
  import { Clock, TriangleAlert } from "@lucide/svelte";
  import { date } from "./api";
  import { now } from "./time";
  let {
    expires,
    label = "Ends",
    warning = "Your files will be deleted when the meeting ends. Save your work now.",
    due = "Time is up. Cleanup is due.",
    compact = false,
  }: {
    expires: string;
    label?: string;
    warning?: string;
    due?: string;
    compact?: boolean;
  } = $props();
  const remaining = $derived(Math.max(0, Date.parse(expires) - $now));
  const minutes = $derived(Math.ceil(remaining / 60000));
  const urgent = $derived(remaining <= 15 * 60000);
</script>

<div class="text-sm">
  <p
    class="flex flex-wrap items-center gap-x-1.5 tabular-nums text-base-content/70"
  >
    <Clock size={14} aria-hidden="true" /><span
      class="font-medium text-base-content"
      class:text-warning={urgent}
      >{remaining === 0
        ? "Time is up"
        : minutes >= 60
          ? `${Math.floor(minutes / 60)} h ${minutes % 60} m left`
          : `${minutes} m left`}</span
    ><span>· {label} <time datetime={expires}>{date(expires)}</time></span>
  </p>
  {#if urgent && !compact}
    <div class="alert alert-soft alert-warning mt-3 text-sm" role="alert">
      <TriangleAlert size={18} aria-hidden="true" /><span
        >{remaining === 0 ? due : "15 minutes or less remain."}
        {warning}</span
      >
    </div>
  {/if}
</div>
