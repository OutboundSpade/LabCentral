<script lang="ts">
  import { ArrowRight, LogIn } from "@lucide/svelte";
  import { session } from "$lib/api";
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  let mounted = $state(false);
  onMount(() => {
    mounted = true;
  });
  $effect(() => {
    if (mounted && $session)
      void goto($session.membership.teamID ? "/lab/" : "/teams/", {
        replaceState: true,
      });
  });
</script>

<svelte:head><title>LabCentral</title></svelte:head>
<section class="mx-auto flex min-h-[60dvh] max-w-sm flex-col justify-center">
  <img
    src="/brand.svg?v=corner-cell"
    width="40"
    height="40"
    alt=""
    class="mb-6 object-contain"
  />
  <h1 class="text-2xl font-semibold tracking-tight">Your lab awaits.</h1>
  <p class="mt-3 text-sm leading-relaxed text-base-content/75">
    {$session
      ? "Choose your team or return to your lab."
      : "Sign in to join your team and open your desktop."}
  </p>
  {#if $session}<a
      href={$session.membership.teamID ? "/lab/" : "/teams/"}
      class="btn btn-sm min-h-11 btn-primary mt-6 w-full"
      >{$session.membership.teamID
        ? "Open your lab"
        : "Choose your team"}<ArrowRight size={16} /></a
    >{:else}<a
      href="/auth/login"
      data-sveltekit-reload
      class="btn btn-sm min-h-11 btn-primary mt-6 w-full"
      >Sign in with CDC ID<LogIn size={16} /></a
    >{/if}
</section>
