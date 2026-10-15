<script lang="ts">
  import { LogIn } from "@lucide/svelte";
  import { session, loading } from "$lib/api";
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import PageHeader from "$lib/PageHeader.svelte";
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
  <section>
    <PageHeader title="Hands-on labs for team practice." description="Guided practice and team meetings.">
      <a href="/auth/login" data-sveltekit-reload class="btn btn-sm btn-primary min-h-11 lg:min-h-8">Sign in<LogIn size={16} /></a>
    </PageHeader>
  </section>
{/if}
