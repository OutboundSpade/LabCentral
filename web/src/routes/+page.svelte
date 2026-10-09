<script lang="ts">
  import { LogIn } from "@lucide/svelte";
  import { session, loading } from "$lib/api";
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import Logo from "$lib/Logo.svelte";
  let mounted = $state(false);
  onMount(() => {
    mounted = true;
  });
  // A live meeting takes priority; otherwise signed-in home is the lab catalog.
  const destination = $derived(
    $session?.meeting && !["ended", "ending"].includes($session.meeting.status)
      ? $session.membership.teamID
        ? "/lab/"
        : "/teams/"
      : "/labs/",
  );
  $effect(() => {
    if (mounted && $session) void goto(destination, { replaceState: true });
  });
</script>

<svelte:head><title>LabCentral</title></svelte:head>
{#if $session || $loading}<p
    class="flex min-h-48 items-center gap-3 text-sm text-base-content/70"
    role="status"
  >
    <span class="loading loading-spinner loading-sm"></span>Opening LabCentral…
  </p>{:else}
  <section class="mx-auto flex max-w-xl flex-col items-center pt-10 text-center sm:pt-20">
    <Logo size={52} />
    <h1 class="mt-8 text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
      Hands-on labs for CDC practice.
    </h1>
    <p class="mt-4 max-w-md text-base leading-relaxed text-base-content/70">
      Work through guided labs at your own pace, and join your team's desktop
      during meetings.
    </p>
    <a
      href="/auth/login"
      data-sveltekit-reload
      class="btn btn-primary mt-8 min-h-11 px-6"
      >Sign in with CDC ID<LogIn size={16} /></a
    >
  </section>
{/if}
