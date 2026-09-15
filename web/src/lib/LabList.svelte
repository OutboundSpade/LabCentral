<script lang="ts">
  import BulkLabControls from "./BulkLabControls.svelte";
  import { ArrowUpRight } from "@lucide/svelte";
  import { type Instance, type Operation } from "./api";
  import StatusBadge from "./StatusBadge.svelte";
  let {
    instances,
    name,
    owner,
    refresh,
  }: {
    instances: Instance[];
    name: (i: Instance) => string;
    owner?: (i: Instance) => string;
    refresh: () => Promise<void>;
  } = $props();
  let selected = $state<string[]>([]),
    busy = $state(false);
  let results = $state<
    Record<string, { operation?: Operation; error?: string; key: string }>
  >({});
  const deletable = (i: Instance) =>
    i.phase !== "destroyed" &&
    (i.desiredLifecycle !== "destroyed" ||
      ["failed", "degraded"].includes(i.phase)) &&
    !results[i.id]?.operation;
</script>

<BulkLabControls
  {instances}
  {name}
  {owner}
  {refresh}
  bind:selected
  bind:results
  bind:busy
/>
<div class="max-w-full overflow-x-auto rounded-box border border-base-300">
  <table class="table table-sm">
    <thead
      ><tr
        ><th><span class="sr-only">Select</span></th><th>Lab</th>{#if owner}<th
            >Owner</th
          >{/if}<th>Status</th><th>Expires</th><th
          ><span class="sr-only">Open</span></th
        ></tr
      ></thead
    >
    <tbody
      >{#each instances as instance (instance.id)}<tr>
          <td
            ><input
              type="checkbox"
              class="checkbox checkbox-sm"
              aria-label={`Select lab ${name(instance)}${owner ? ` for ${owner(instance)}` : ""}`}
              value={instance.id}
              bind:group={selected}
              disabled={busy || !deletable(instance)}
            /></td
          >
          <td
            ><a
              class="font-medium hover:underline"
              href={`/lab/?id=${instance.id}`}>{name(instance)}</a
            >
            {#if results[instance.id]?.error}<p
                role="alert"
                class="mt-1 max-w-sm whitespace-normal text-error"
              >
                {results[instance.id].error}
              </p>{/if}
          </td>
          {#if owner}<td>{owner(instance)}</td>{/if}
          <td
            ><StatusBadge
              state={instance.phase}
            />{#if results[instance.id]?.operation && instance.phase !== "destroyed"}<span
                class="ml-2 text-xs">Deletion queued</span
              >{/if}</td
          >
          <td
            >{instance.phase === "destroyed"
              ? "—"
              : instance.expiresAt
                ? new Date(instance.expiresAt).toLocaleString([], {
                    month: "short",
                    day: "numeric",
                    hour: "2-digit",
                    minute: "2-digit",
                  })
                : "No expiry"}</td
          >
          <td
            ><a
              class="btn btn-sm btn-ghost"
              aria-label={`Open ${name(instance)}`}
              href={`/lab/?id=${instance.id}`}><ArrowUpRight size={16} /></a
            ></td
          >
        </tr>{/each}</tbody
    >
  </table>
</div>
