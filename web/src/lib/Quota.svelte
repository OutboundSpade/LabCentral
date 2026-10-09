<script lang="ts">
  import type { CloudQuota } from "./types";
  import { date } from "./api";
  import { hours } from "./learning";
  let { quota }: { quota: CloudQuota } = $props();
  const limit = $derived(Math.max(0, quota.limitMinutes * 60));
  const used = $derived(Math.min(limit, Math.max(0, quota.usedSeconds)));
  const resets = $derived(
    date(quota.resetsAt, { weekday: "short", hour: "numeric", minute: "2-digit" }),
  );
</script>

<div
  class="min-w-0 text-sm"
  title={`${hours(used)} used. Resets ${resets}. Time counts until cleanup is confirmed.`}
>
  <p class="tabular-nums">
    <span class="font-medium">{hours(quota.remainingSeconds)}</span>
    <span class="text-base-content/70">cloud time left</span>
  </p>
  <progress
    class="progress progress-primary mt-1.5 h-1 w-full min-w-32"
    value={limit - used}
    max={limit || 1}
    aria-label="Cloud time left this week"
    aria-valuetext={`${hours(quota.remainingSeconds)} left of ${hours(limit)}, resets ${resets}`}
  ></progress>
</div>
