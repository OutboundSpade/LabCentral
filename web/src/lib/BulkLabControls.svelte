<script lang="ts">
  import { onMount } from "svelte";
  import { Trash2 } from "@lucide/svelte";
  import {
    api,
    mutationHeaders,
    terminal,
    type Instance,
    type Operation,
  } from "./api";
  import StatusBadge from "./StatusBadge.svelte";
  let {
    instances,
    name,
    owner,
    refresh,
    selected = $bindable([]),
    results = $bindable({}),
    busy = $bindable(false),
    label = "Delete selected",
  }: {
    instances: Instance[];
    name: (i: Instance) => string;
    owner?: (i: Instance) => string;
    refresh: () => Promise<void>;
    selected?: string[];
    results?: Record<
      string,
      { operation?: Operation; error?: string; key: string }
    >;
    busy?: boolean;
    label?: string;
  } = $props();
  let confirmation = $state<Instance[]>([]);
  let dialog = $state<HTMLDialogElement>();
  let message = $state("");
  const deletable = (i: Instance) =>
    i.phase !== "destroyed" &&
    (i.desiredLifecycle !== "destroyed" ||
      ["failed", "degraded"].includes(i.phase)) &&
    !results[i.id]?.operation;
  let eligible = $derived(instances.filter(deletable));
  let selection = $derived(eligible.filter((i) => selected.includes(i.id)));
  let disposed = false;
  let timer: ReturnType<typeof setTimeout>;
  onMount(() => {
    const poll = async () => {
      for (const [id, result] of Object.entries(results)) {
        if (!result.operation || terminal(result.operation.state)) continue;
        try {
          const operation = await api<Operation>(
            `/operations/${result.operation.id}`,
          );
          if (disposed) return;
          results[id] = { ...result, operation };
          if (terminal(operation.state) && operation.state !== "succeeded") {
            results[id] = {
              key: mutationHeaders()["Idempotency-Key"],
              error:
                operation.message ||
                "Deletion failed. Select this lab to retry.",
            };
          }
        } catch {
          /* Existing lab polling retains the last known phase. */
        }
      }
      if (!disposed) timer = setTimeout(poll, 3000);
    };
    timer = setTimeout(poll, 3000);
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  });
  function confirm() {
    confirmation = [...selection];
    dialog?.showModal();
  }
  async function destroy() {
    if (busy || !confirmation.length) return;
    busy = true;
    const pending = [...confirmation];
    let accepted = 0,
      failed = 0;
    // Four independent existing API commands; keys survive uncertain response retries.
    await Promise.all(
      Array.from({ length: Math.min(4, pending.length) }, async () => {
        while (pending.length) {
          const lab = pending.shift()!;
          const key =
            results[lab.id]?.key ?? mutationHeaders()["Idempotency-Key"];
          try {
            const response = await api<{ operation: Operation }>(
              `/lab-instances/${encodeURIComponent(lab.id)}/actions/destroy`,
              {
                method: "POST",
                headers: { "Idempotency-Key": key },
                body: "{}",
              },
            );
            results[lab.id] = { operation: response.operation, key };
            accepted++;
            selected = selected.filter((id) => id !== lab.id);
          } catch (cause) {
            results[lab.id] = {
              error:
                cause instanceof Error
                  ? cause.message
                  : "Deletion could not be requested.",
              key,
            };
            failed++;
          }
        }
      }),
    );
    message = `${accepted} deletion${accepted === 1 ? "" : "s"} queued.${failed ? ` ${failed} failed. Select those labs to retry.` : ""}`;
    dialog?.close();
    busy = false;
    await refresh();
  }
</script>

<div class="mb-4 flex flex-wrap items-center justify-between gap-3">
  <label class="flex items-center gap-2 text-sm">
    <input
      type="checkbox"
      class="checkbox checkbox-sm"
      aria-label="Select all active labs"
      disabled={busy || !eligible.length}
      checked={eligible.length > 0 && selection.length === eligible.length}
      indeterminate={selection.length > 0 && selection.length < eligible.length}
      onchange={(event) => {
        selected = event.currentTarget.checked ? eligible.map((i) => i.id) : [];
      }}
    />
    {selection.length
      ? `${selection.length} selected`
      : `${instances.length} labs`}
  </label>
  <button
    class="btn btn-sm"
    disabled={busy || !selection.length}
    onclick={confirm}><Trash2 size={16} />{label}</button
  >
</div>
{#if message}<p role="status" class="mb-3 text-sm">{message}</p>{/if}
<dialog
  bind:this={dialog}
  class="modal"
  aria-labelledby="bulk-delete-title"
  oncancel={(event) => {
    if (busy) event.preventDefault();
  }}
>
  <div class="modal-box">
    <h2 id="bulk-delete-title" class="text-lg font-semibold">
      Delete {confirmation.length} labs?
    </h2>
    <p class="mt-3 text-sm text-base-content/70">
      This permanently deletes these labs and their files.
    </p>
    <ul class="my-4 max-h-60 overflow-y-auto space-y-2 text-sm">
      {#each confirmation as lab}<li>
          {name(lab)}{owner ? ` · ${owner(lab)}` : ""}
        </li>{/each}
    </ul>
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm" disabled={busy}>Cancel</button>
      </form>
      <button class="btn btn-sm btn-error" disabled={busy} onclick={destroy}
        >{busy ? "Deleting…" : "Delete labs and files"}</button
      >
    </div>
  </div>
</dialog>
