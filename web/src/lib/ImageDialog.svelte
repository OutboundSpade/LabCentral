<script lang="ts">
  import { api, refreshAdmin } from "./api";
  import { templateName } from "./display";
  import Notice from "./Notice.svelte";
  import type { Template } from "./types";
  // Confirms image builds, per-revision image removal and unused-image removal.
  type Action = "build" | "remove-images" | "prune-images";
  let dialog: HTMLDialogElement;
  let action = $state<Action>("build");
  let revision = $state<Template | null>(null);
  let unused = $state(0);
  let busy = $state(false);
  let error = $state("");
  export function show(next: Action, template: Template | null = null, count = 0) {
    action = next;
    revision = template;
    unused = count;
    error = "";
    dialog.showModal();
  }
  async function run() {
    busy = true;
    error = "";
    try {
      if (action === "prune-images") await api("/api/admin/images/prune", {});
      else await api("/api/admin/templates/" + action, { id: revision?.id });
      dialog.close();
      await refreshAdmin().catch(() => {});
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<dialog bind:this={dialog} class="modal" aria-labelledby="image-dialog-title">
  <div class="modal-box">
    <h2 id="image-dialog-title" class="text-lg font-semibold">
      {action === "prune-images"
        ? "Remove unused images?"
        : action === "build"
          ? "Build images?"
          : "Remove images?"}
    </h2>
    {#if revision}<p class="mt-1 text-sm text-base-content/70">
        {templateName(revision.name)} · <code>{revision.commit.slice(0, 7)}</code>
      </p>{/if}
    <p class="mt-3 text-sm leading-relaxed">
      {action === "prune-images"
        ? `Removes ${unused} unused ${unused === 1 ? "image" : "images"}. Latest revisions, open meetings, active practice and builds are kept.`
        : action === "build"
          ? `Builds ${revision?.imagesTotal ?? 0} images. Temporary build machines incur charges; matching images are reused.`
          : "Removes this revision’s unused images and unfinished builds. Shared images are kept, and the revision can be rebuilt."}
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <button type="button" class="btn btn-sm min-h-11" onclick={() => dialog.close()}
        >Cancel</button
      >
      <button
        class={["btn btn-sm min-h-11", action === "build" ? "btn-primary" : "btn-error"]}
        disabled={busy}
        onclick={run}
        >{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}{action ===
        "build"
          ? "Start build"
          : action === "prune-images"
            ? "Remove unused images"
            : "Remove images"}</button
      >
    </div>
  </div>
  <form method="dialog" class="modal-backdrop"><button tabindex="-1">Close</button></form>
</dialog>
