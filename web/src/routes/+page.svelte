<script lang="ts">
  import { LogIn, Users } from "@lucide/svelte";
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
  const path = [
    {
      title: "Choose a pinned lab",
      text: "Each published lab is fixed to one template revision, so the instructions match the machines you work on.",
    },
    {
      title: "Run it in the cloud or on your own",
      text: "Start a personal cloud desktop from your weekly allowance, or download the portable source for the LabCentral CLI.",
    },
    {
      title: "Follow the stage rail",
      text: "Correct answers and verified workspace actions advance each stage. Instructors prepare team labs ahead of time and choose template stages in any order.",
    },
  ];
</script>

<svelte:head><title>LabCentral</title></svelte:head>
{#if $session || $loading}<p
    class="flex min-h-48 items-center gap-3 text-sm text-base-content/75"
    role="status"
  >
    <span class="loading loading-spinner loading-sm"></span>Opening LabCentral…
  </p>{:else}
  <section
    class="grid items-start gap-10 pt-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,27rem)] lg:items-center lg:gap-16 lg:pt-8 xl:gap-20"
  >
    <div class="min-w-0">
      <Logo size={44} />
      <h1
        class="mt-6 max-w-xl text-3xl font-semibold leading-tight tracking-tight sm:text-4xl"
      >
        Practice labs that stay open after the meeting.
      </h1>
      <p class="mt-4 max-w-lg text-base leading-relaxed text-base-content/80">
        Work through guided labs at your own pace. Instructions sit beside your
        workspace, and each stage advances when your answer or workspace check
        passes.
      </p>
      <a
        href="/auth/login"
        data-sveltekit-reload
        class="btn min-h-11 btn-primary mt-7"
        >Sign in with CDC ID<LogIn size={16} /></a
      >
      <p class="mt-3 text-xs text-base-content/75">
        Use the same account you use for team meetings.
      </p>
    </div>
    <ol class="min-w-0" aria-label="How practice labs work">
      {#each path as item, index}<li class="relative pb-7 pl-11 last:pb-0">
          {#if index < path.length - 1}<span
              aria-hidden="true"
              class="absolute top-8 bottom-0 left-3.5 w-px bg-primary/45"
            ></span>{/if}
          <span
            aria-hidden="true"
            class="absolute top-0 left-0 grid size-7 place-items-center rounded-full border border-base-content/25 bg-base-200 text-xs font-medium tabular-nums"
            >{index + 1}</span
          >
          <h2 class="pt-0.5 text-sm font-semibold">{item.title}</h2>
          <p class="mt-1.5 text-sm leading-relaxed text-base-content/75">
            {item.text}
          </p>
        </li>{/each}
    </ol>
  </section>
  <section
    class="mt-12 flex max-w-3xl items-start gap-3 border-t border-base-content/10 pt-6 text-sm leading-relaxed"
  >
    <Users size={17} class="mt-0.5 shrink-0 text-base-content/75" aria-hidden="true" />
    <p>
      <span class="font-medium">Team meetings continue here.</span>
      <span class="text-base-content/75"
        >During a live meeting, choose your team and open its shared lab
        desktop. Meeting labs are removed when the meeting ends; practice labs
        remain available.</span
      >
    </p>
  </section>
{/if}
