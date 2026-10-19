<script lang="ts">
  import { onMount } from "svelte";
  import { api, refresh } from "$lib/api";
  import type { PracticeSettings } from "$lib/types";
  import Notice from "$lib/Notice.svelte";
  let settings = $state<PracticeSettings | null>(null);
  let busy = $state(false);
  let error = $state("");
  let success = $state("");
  let confirmation = $state<HTMLDialogElement>();
  async function load() {
    settings = await api<PracticeSettings>("/api/admin/practice");
  }
  onMount(() => {
    void load().catch(e => error = e.message);
    const timer = setInterval(() => {
      if (busy || confirmation?.open || !settings) return;
      const previous = settings.confirmation;
      void api<PracticeSettings>("/api/admin/practice").then(next => {
        // A background response must never replace a list being reviewed.
        if (!busy && !confirmation?.open && settings?.confirmation === previous) settings = next;
      }).catch(() => {});
    }, 10000);
    return () => clearInterval(timer);
  });
  async function review() {
    busy = true;
    error = "";
    success = "";
    try {
      await load();
      confirmation?.showModal();
    } catch (e) { error = (e as Error).message; }
    finally { busy = false; }
  }
  async function save(enabled: boolean) {
    if (!settings) return;
    busy = true;
    error = "";
    try {
      settings = await api<PracticeSettings>("/api/admin/practice", {enabled, confirmation: settings.confirmation});
      confirmation?.close();
      success = enabled ? "Practice enabled. Previous labs will not restart." : "Practice disabled. Cleanup is queued for the listed labs.";
      await refresh();
    } catch (e) {
      error = (e as Error).message;
      // Never silently replace the reviewed list and submit a new confirmation.
    } finally { busy = false; }
  }
</script>

<section class="mb-8 rounded-box border border-base-content/10 p-5" aria-labelledby="practice-setting-title">
  <div class="flex flex-wrap items-center justify-between gap-4">
    <div class="min-w-0">
      <h2 id="practice-setting-title" class="text-sm font-semibold">Practice labs</h2>
      <p class="mt-1 max-w-xl text-sm text-base-content/70">{settings ? (settings.enabled ? "Enabled for everyone." : "Disabled for everyone.") : "Loading settings…"} Team meetings remain available.</p>
    </div>
    {#if settings}<button type="button" class="btn btn-sm min-h-11" disabled={busy} onclick={review}>{settings.enabled ? "Disable practice…" : "Enable practice…"}</button>{/if}
  </div>
  {#if error && !confirmation?.open}<Notice message={error} />{/if}
  {#if success}<p class="mt-3 text-sm" role="status">{success}</p>{/if}
  {#if settings && !settings.enabled && settings.activeLabs.length}<p class="mt-3 text-sm text-warning" role="status">{settings.activeLabs.length} practice {settings.activeLabs.length === 1 ? "lab awaits" : "labs await"} confirmed cleanup. Check live activity for failures. Re-enabling practice does not cancel cleanup.</p>{/if}
</section>
<dialog bind:this={confirmation} class="modal" aria-labelledby="practice-confirm-title" oncancel={(event) => { if (busy) event.preventDefault(); }}>
  <div class="modal-box max-h-[85dvh] overflow-y-auto">
    <h2 id="practice-confirm-title" class="text-lg font-semibold">{settings?.enabled ? "Are you sure you want to disable practice?" : "Enable practice labs?"}</h2>
    {#if settings?.enabled}
      <p class="mt-3 text-sm leading-relaxed">This blocks practice browsing, downloads, attempts and provisioning for everyone, including administrators. Practice desktops will disconnect. Team meeting labs are unaffected.</p>
      <h3 class="mt-5 text-sm font-semibold">Labs to deprovision ({settings.activeLabs.length})</h3>
      {#if settings.activeLabs.length}
        <ul class="mt-2 divide-y divide-base-content/10">
          {#each settings.activeLabs as lab (lab.id)}<li class="py-3 text-sm">
            <p class="break-words font-medium">{lab.lab}</p>
            <p class="break-words text-base-content/70">{lab.user} · {lab.status}</p>
            <p class="mt-1 break-all font-mono text-xs text-base-content/60">{lab.id}</p>
          </li>{/each}
        </ul>
        <p class="mt-3 text-sm text-warning">Machines and workspace files will be deleted. This cannot be undone. Cleanup may take time and remains queued until deletion is confirmed.</p>
      {:else}<p class="mt-2 text-sm text-base-content/70">No active practice labs.</p>{/if}
    {:else}<p class="mt-3 text-sm leading-relaxed">Everyone can browse and start practice again. Previously ended labs will not restart, and pending cleanup will continue.</p>{/if}
    <Notice message={error} />
    <div class="modal-action flex-wrap">
      <form method="dialog"><button class="btn btn-sm min-h-11" disabled={busy}>Cancel</button></form>
      {#if error}<button class="btn btn-sm min-h-11" disabled={busy} onclick={review}>Review current list</button>{/if}
      <button class="btn btn-sm min-h-11" class:btn-error={settings?.enabled} class:btn-primary={!settings?.enabled} disabled={busy || !!error || !settings} onclick={() => save(!settings?.enabled)}>{busy ? "Saving…" : settings?.enabled ? "Disable practice and delete labs" : "Enable practice"}</button>
    </div>
  </div>
</dialog>
