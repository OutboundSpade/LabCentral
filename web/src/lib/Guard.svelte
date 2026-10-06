<script lang="ts">
  import type { Snippet } from "svelte";
  import { LogIn, ShieldCheck } from "@lucide/svelte";
  import { session, loading, loadError, refresh } from "./api";
  import Notice from "./Notice.svelte";
  let { admin = false, children }: { admin?: boolean; children: Snippet } =
    $props();
</script>

{#if $loading}<div class="flex min-h-48 items-center gap-3" role="status">
    <span class="loading loading-spinner loading-sm"></span>Loading your
    workspace…
  </div>
{:else if $loadError}<Notice message={$loadError} /><button
    class="btn btn-sm min-h-11"
    onclick={refresh}>Retry</button
  >
{:else if !$session}<section
    class="max-w-lg border-t border-base-content/10 py-8"
  >
    <LogIn size={24} aria-hidden="true" />
    <h2 class="mt-4 text-lg font-semibold">Sign in to continue</h2>
    <p class="mt-2 mb-5 text-sm leading-relaxed text-base-content/75">
      Use your CDC account to join a team and access your lab.
    </p>
    <a
      href="/auth/login"
      data-sveltekit-reload
      class="btn btn-sm min-h-11 btn-primary"
      >Continue with CDC ID <LogIn size={16} /></a
    >
  </section>
{:else if admin && !$session.user.admin}<section
    class="border-t border-base-content/10 py-8"
  >
    <ShieldCheck size={24} />
    <h2 class="mt-4 text-lg font-semibold">For meeting administrators</h2>
    <p class="mt-2 text-sm text-base-content/75">
      You can access your team's workspace from <a class="link" href="/lab/"
        >Your lab</a
      >.
    </p>
  </section>
{:else}{@render children()}{/if}
