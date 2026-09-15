<script lang="ts">
  import { Server, Terminal } from "@lucide/svelte";
  import StatusBadge from "./StatusBadge.svelte";
  import type { Instance } from "./api";
  let {
    detail,
    name,
    expires,
    instructions,
    administrator,
    browserConsole,
    busy,
    onopen,
    onend,
  }: {
    detail?: Instance;
    name: string;
    expires: string;
    instructions?: string;
    administrator: boolean;
    browserConsole: boolean;
    busy: boolean;
    onopen: (instance: Instance, resource: string) => void;
    onend: (instance: Instance) => void;
  } = $props();
</script>

{#if detail}<section class="rounded-box border border-base-300 bg-base-200 p-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <h2 class="text-xl font-semibold capitalize">
        {name}
      </h2>
      <StatusBadge state={detail.phase} />
    </div>
    <p class="mt-3 text-sm text-base-content/70">
      Expires {expires}. Files in this lab are deleted when it ends.
    </p>
    {#if instructions}<section
        class="mt-6 rounded-box border border-base-300 p-5"
      >
        <h3 class="font-semibold">Lab instructions</h3>
        <p
          class="mt-3 whitespace-pre-wrap text-sm leading-6 text-base-content/80"
        >
          {instructions}
        </p>
      </section>{/if}{#if detail.errorMessage}<p
        role="alert"
        class="mt-4 text-error"
      >
        {detail.errorMessage}
      </p>{/if}
    <div class="mt-6 divide-y divide-base-300">
      {#each detail.resources?.filter((r) => administrator || r.kind === "VM" || r.kind === "Container") ?? [] as resource}<div
          class="flex flex-wrap items-center justify-between gap-4 py-4"
        >
          <div class="flex items-center gap-3">
            <Server size={19} />
            <div>
              <h3 class="font-medium">
                {resource.id.split(".").slice(-1)[0]}
              </h3>
              <p class="text-sm text-base-content/70">
                {resource.kind}
              </p>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <StatusBadge
              state={resource.phase}
            />{#if browserConsole && (resource.kind === "VM" || resource.virtualMachine) && resource.phase === "ready"}<button
                class="btn btn-primary"
                disabled={busy}
                onclick={() => onopen(detail!, resource.id)}
                ><Terminal size={16} />Open console</button
              >{/if}
          </div>
        </div>{/each}
    </div>
    {#if !browserConsole}<p class="mt-4 text-sm text-base-content/70">
        Browser consoles are not available on this installation. Contact your
        instructor for access.
      </p>{/if}

    {#if !["destroying", "destroyed"].includes(detail.phase)}<div
        class="mt-8 border-t border-base-300 pt-5"
      >
        <button
          class="btn btn-error btn-soft"
          disabled={busy}
          onclick={() => onend(detail!)}>End lab</button
        >
      </div>{/if}
  </section>{:else}<p class="text-base-content/70">
    This lab is unavailable or you no longer have access.
  </p>{/if}
