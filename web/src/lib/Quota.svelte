<script lang="ts">
  import { Cloud } from "@lucide/svelte";
  import type { CloudQuota } from "./types";
  import { date } from "./api";
  import { hours } from "./learning";
  let { quota }: { quota: CloudQuota } = $props();
  const uid = $props.id();
  const limit = $derived(Math.max(0, quota.limitMinutes * 60));
  const used = $derived(Math.min(limit, Math.max(0, quota.usedSeconds)));
</script>

<section aria-labelledby={`${uid}-title`} class="min-w-0">
  <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
    <p
      id={`${uid}-title`}
      class="flex items-center gap-2 text-sm font-medium"
    >
      <Cloud size={15} aria-hidden="true" />Cloud time this week
    </p>
    <p class="text-sm tabular-nums">
      <span class="font-medium">{hours(quota.remainingSeconds)}</span> left of
      {hours(limit)}
    </p>
  </div>
  <progress
    class="progress mt-2 h-1.5 w-full"
    value={used}
    max={limit || 1}
    aria-label="Cloud time used this week"
    aria-valuetext={`${hours(used)} used of ${hours(limit)}`}
  ></progress>
  <p class="mt-1.5 text-xs text-base-content/75">
    {hours(used)} used · resets
    <time datetime={quota.resetsAt}
      >{date(quota.resetsAt, {
        weekday: "short",
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      })}</time
    > (start of the UTC week). Time counts from allocation until cleanup is confirmed.
  </p>
</section>
