<script lang="ts">
  import { CircleCheck, CircleAlert, Clock } from "@lucide/svelte";
  let { state }: { state: string } = $props();
  const labels: Record<string, string> = {
    ready: "Ready",
    pending: "Queued",
    applying: "Preparing",
    provisioning: "Preparing",
    destroying: "Ending",
    destroyed: "Ended",
    failed: "Needs attention",
    cleanup_failed: "Cleanup needed",
    succeeded: "Complete",
    running: "In progress",
    queued: "Queued",
  };
  let good = $derived(["ready", "succeeded"].includes(state));
  let failed = $derived(
    ["failed", "cleanup_failed", "unknown", "partial", "timed_out"].includes(
      state,
    ),
  );
</script>

<span
  class="badge badge-sm badge-soft gap-1.5 whitespace-nowrap"
  class:badge-error={failed}
>
  {#if good}<CircleCheck size={12} />{:else if failed}<CircleAlert
      size={12}
    />{:else}<Clock size={12} />{/if}
  {labels[state] ?? state.replaceAll("_", " ")}
</span>
