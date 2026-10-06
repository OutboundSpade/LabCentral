<script lang="ts">
  import { Check, Clock, TriangleAlert, CircleMinus } from "@lucide/svelte";
  let {
    status,
    label,
    soft = false,
  }: { status: string; label?: string; soft?: boolean } = $props();
  const good = $derived(["ready", "passed"].includes(status));
  const bad = $derived(["failed", "unavailable"].includes(status));
  const working = $derived(
    ["running", "preparing", "checking", "resetting", "applying"].includes(
      status,
    ),
  );
  const caution = $derived(["partial", "queued", "pending"].includes(status));
  const names: Record<string, string> = {
    ready: "Ready",
    waiting: "Not prepared",
    passed: "Pass",
    failed: "Failed",
    unavailable: "Unavailable",
    complete: "Complete",
    running: "Running",
    preparing: "Preparing",
    checking: "Checking",
    resetting: "Resetting",
    applying: "Applying",
    partial: "Needs attention",
    queued: "Queued",
    pending: "Pending",
    ended: "Ended",
    ending: "Ending",
    paused: "Paused",
  };
</script>

<span
  class={[
    "inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-xs",
    soft ? "badge badge-soft badge-sm h-auto min-h-6 py-1" : "",
  ]}
  class:badge-success={soft && good}
  class:badge-warning={soft && caution}
  class:badge-error={soft && bad}
  class:text-error={bad}
>
  {#if good}<Check
      size={13}
      class="text-success"
      aria-hidden="true"
    />{:else if bad}<TriangleAlert
      size={13}
      aria-hidden="true"
    />{:else if working}<span
      class="loading loading-spinner loading-xs motion-reduce:animate-none"
      aria-hidden="true"
    ></span>{:else if caution}<Clock
      size={13}
      class="text-warning"
      aria-hidden="true"
    />{:else}<CircleMinus
      size={13}
      class="text-base-content/75"
      aria-hidden="true"
    />{/if}
  {label ?? names[status] ?? status}
</span>
