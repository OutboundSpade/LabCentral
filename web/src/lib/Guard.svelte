<script lang="ts">
  import type { Snippet } from "svelte";
  import { LogIn, ShieldCheck, RotateCw } from "@lucide/svelte";
  import { session, loading, loadError, refresh } from "./api";
  import Notice from "./Notice.svelte";
  import EmptyState from "./EmptyState.svelte";
  let { admin = false, children }: { admin?: boolean; children: Snippet } =
    $props();
</script>

{#if $loading}<div
    class="flex min-h-48 items-center gap-3 text-sm text-base-content/75"
    role="status"
  >
    <span class="loading loading-spinner loading-sm"></span>Loading your
    workspace…
  </div>
{:else if $loadError && !$session}<Notice message={$loadError} /><button
    class="btn btn-outline btn-sm min-h-11"
    onclick={refresh}><RotateCw size={15} />Retry</button
  >
{:else if !$session}<EmptyState icon={LogIn} title="Sign in to continue">
    Sign in to open practice labs, join a team and access your
    desktop.
    {#snippet actions()}<a
        href="/auth/login"
        data-sveltekit-reload
        class="btn btn-sm min-h-11 btn-primary"
        >Sign in <LogIn size={16} /></a
      >{/snippet}
  </EmptyState>
{:else if admin && !$session.user.admin}<EmptyState
    icon={ShieldCheck}
    title="For meeting administrators"
  >
    You can practice from <a class="link" href="/labs/">Labs</a> or open your
    team's workspace from <a class="link" href="/lab/">Your lab</a>.
  </EmptyState>
{:else}{@render children()}{/if}
