<script lang="ts">
  import { Hammer, LockKeyhole, Trash2, Users } from "@lucide/svelte";
  import { onMount } from "svelte";
  import { adminView, date, refreshAdmin } from "./api";
  import {
    buildable,
    buildLabel,
    jobActive,
    jobName,
    revisionJob,
    revisionState,
  } from "./job";
  import Status from "./Status.svelte";
  import MeetingDialog from "./MeetingDialog.svelte";
  import ImageDialog from "./ImageDialog.svelte";
  // Administrator controls for one pinned revision on its detail page.
  let { id }: { id: string } = $props();
  let meetingDialog: MeetingDialog;
  let imageDialog: ImageDialog;
  const data = $derived($adminView);
  const t = $derived(data?.templates.find((x) => x.id === id));
  const job = $derived(revisionJob(data, id));
  const working = $derived(jobActive(job));
  const current = $derived(t ? revisionState(t, job) : null);
  const meetingOpen = $derived(!!data?.meeting && data.meeting.status !== "ended");
  onMount(() => {
    if (!$adminView) void refreshAdmin().catch(() => {});
  });
</script>

<section
  aria-labelledby="revision-admin-title"
  class="rounded-box border border-base-content/9 bg-base-200/70 p-4"
>
  <h3 id="revision-admin-title" class="text-xs font-medium text-base-content/70">
    Administration
  </h3>
  {#if t && current}
    <p class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1" aria-live="polite">
      <Status status={current.status} label={current.label} />
      {#if !t.removedAt}<span class="text-xs tabular-nums text-base-content/60"
          >{t.imagesReady} / {t.imagesTotal} images</span
        >{/if}
    </p>
    {#if t.built && t.builtAt}<p class="mt-1 text-xs text-base-content/60">
        Built {date(t.builtAt)}
      </p>{/if}
    {#if job?.waitingOn?.length}<p class="mt-2 text-xs text-base-content/70">
        Waiting for {job.waitingOn
          .map((w) => jobName(data?.jobs.find((j) => j.id === w)?.kind ?? ""))
          .join(", ")}
      </p>{/if}
    {#if job?.status === "failed" && job.error}<p class="mt-2 break-words text-sm text-error">
        {job.error}
      </p>{/if}
    <div class="mt-3 flex flex-wrap gap-2">
      {#if t.built && !t.removing && !t.retired && !meetingOpen}<button
          class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
          onclick={() => meetingDialog.show(t)}><Users size={15} />Start meeting</button
        >{/if}
      {#if buildable(t)}<button
          class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
          disabled={working}
          onclick={() => imageDialog.show("build", t)}
          ><Hammer size={15} />{buildLabel(t, job)}</button
        >{/if}
      {#if t.imagesReady > 0 || t.buildsPending > 0 || t.removing || (job?.kind === "build" && job.status === "failed")}<button
          class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8"
          disabled={working || !!t.removalBlocked}
          onclick={() => imageDialog.show("remove-images", t)}
          ><Trash2 size={15} />{t.removing ? "Retry cleanup" : "Remove images"}</button
        >{/if}
    </div>
    {#if t.removalBlocked}<p class="mt-2 flex items-start gap-2 text-xs text-base-content/70">
        <LockKeyhole size={13} class="mt-0.5 shrink-0" aria-hidden="true" />{t.removalBlocked}
      </p>{:else if meetingOpen && t.built && !t.retired}<p class="mt-2 text-xs text-base-content/70">
        A meeting is running. <a class="link" href="/admin/">Open meeting</a>
      </p>{/if}
  {:else if data}<p class="mt-2 text-sm text-base-content/70">Revision unavailable.</p>
  {:else}<p class="mt-2 text-sm text-base-content/70" role="status">Loading…</p>{/if}
</section>
<MeetingDialog bind:this={meetingDialog} />
<ImageDialog bind:this={imageDialog} />
