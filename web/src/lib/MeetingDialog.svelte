<script lang="ts">
  import { goto } from "$app/navigation";
  import { Plus } from "@lucide/svelte";
  import { api, adminView, refreshAdmin } from "./api";
  import { templateName } from "./display";
  import Notice from "./Notice.svelte";
  import type { Meeting } from "./types";
  // Creates a meeting pinned to one built template revision.
  type Revision = { id: string; name: string; commit: string };
  let dialog: HTMLDialogElement;
  let revision = $state<Revision | null>(null);
  let name = $state("");
  let teamCount = $state(5);
  let capacity = $state(6);
  let hours = $state(4);
  let busy = $state(false);
  let error = $state("");
  // The pin must still be built when the form is submitted.
  const available = $derived(
    !!revision &&
      !!$adminView?.templates.some((t) => t.id === revision?.id && t.built && !t.removing),
  );
  export function show(next: Revision) {
    revision = next;
    name = templateName(next.name);
    error = "";
    dialog.showModal();
  }
  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!revision || busy) return;
    if (!available) {
      error = "This revision is no longer built. Choose another template.";
      return;
    }
    busy = true;
    error = "";
    try {
      await api<Meeting>("/api/admin/meeting", {
        name,
        template: revision.id,
        hours,
        teamCount,
        capacity,
      });
      dialog.close();
      await refreshAdmin().catch(() => {});
      await goto("/admin/");
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<dialog bind:this={dialog} class="modal" aria-labelledby="meeting-dialog-title">
  <form class="modal-box" onsubmit={create}>
    <h2 id="meeting-dialog-title" class="text-lg font-semibold">Start a meeting</h2>
    {#if revision}<p class="mt-1 text-sm text-base-content/70">
        {templateName(revision.name)} · <code>{revision.commit.slice(0, 7)}</code>
      </p>{/if}
    <fieldset class="fieldset mt-4 gap-1.5 p-0 text-sm" disabled={busy}>
      <legend class="sr-only">Meeting settings</legend>
      <label class="label" for="meeting-name">Name</label><input
        class="input input-sm min-h-11 w-full"
        id="meeting-name"
        bind:value={name}
        maxlength="100"
        required
      />
      <div class="mt-3 grid gap-4 sm:grid-cols-3">
        <div>
          <label class="label mb-1.5" for="team-count">Number of teams</label><input
            id="team-count"
            class="input input-sm min-h-11 w-full"
            type="number"
            min="1"
            max="100"
            step="1"
            bind:value={teamCount}
            required
          />
        </div>
        <div>
          <label class="label mb-1.5" for="capacity">Students per team</label><input
            id="capacity"
            class="input input-sm min-h-11 w-full"
            type="number"
            min="1"
            max="100"
            step="1"
            bind:value={capacity}
            required
          />
        </div>
        <div>
          <label class="label mb-1.5" for="duration">Duration (hours)</label><input
            id="duration"
            class="input input-sm min-h-11 w-full"
            type="number"
            min="1"
            max="8"
            step="1"
            bind:value={hours}
            required
          />
        </div>
      </div>
      <p class="mt-2 text-xs text-base-content/60">
        {(teamCount || 0) * (capacity || 0)} desktop places · deleted after {hours || 0}
        {hours === 1 ? "hour" : "hours"}
      </p>
    </fieldset>
    <Notice message={error} />
    <div class="modal-action">
      <button type="button" class="btn btn-sm min-h-11" disabled={busy} onclick={() => dialog.close()}
        >Cancel</button
      >
      <button class="btn btn-sm btn-primary min-h-11" disabled={busy || !available}
        >{#if busy}<span class="loading loading-spinner loading-xs"></span>{:else}<Plus
            size={16}
          />{/if}Create meeting</button
      >
    </div>
  </form>
  <form method="dialog" class="modal-backdrop"><button tabindex="-1">Close</button></form>
</dialog>
