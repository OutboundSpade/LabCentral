<script lang="ts">
  import { TriangleAlert, RotateCw } from "@lucide/svelte";
  import { adminPollError, refreshAdmin } from "./api";
</script>

{#if $adminPollError}
  <div class="alert alert-soft alert-warning my-4 text-sm" role="status">
    <TriangleAlert size={18} aria-hidden="true" /><span>{$adminPollError}</span>
    <button
      type="button"
      class="btn btn-outline btn-sm min-h-11"
      onclick={() => refreshAdmin().catch(() => {})}
      ><RotateCw size={15} />Retry update</button
    >
  </div>
{/if}
