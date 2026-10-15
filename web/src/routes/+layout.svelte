<script lang="ts">
  import "../app.css";
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import {
    Monitor,
    SlidersHorizontal,
    LogOut,
    ArrowUpRight,
    FlaskConical,
  } from "@lucide/svelte";
  import {
    session,
    loading,
    refresh,
    refreshAdmin,
    adminView,
    api,
  } from "$lib/api";
  import { get } from "svelte/store";
  import { clearAttemptRequests } from "$lib/library";
  import Notice from "$lib/Notice.svelte";
  import Logo from "$lib/Logo.svelte";
  let { children } = $props();
  let error = $state("");
  onMount(() => {
    void refresh();
    const t = setInterval(refresh, 10000);
    const updates = setInterval(() => {
      if (get(session)?.user.admin) void refreshAdmin().catch(() => {});
    }, 2000);
    return () => {
      clearInterval(t);
      clearInterval(updates);
    };
  });
  async function logout() {
    try {
      await api("/auth/logout", {});
      // Confirmed sign-out: invalidate identity guards before the hard navigation.
      clearAttemptRequests();
      session.set(null);
      location.assign("/");
    } catch (e) {
      error = (e as Error).message;
      await refresh();
    }
  }
  const meetingOpen = $derived(
    !!$session?.meeting &&
      !["ended", "ending"].includes($session.meeting.status),
  );
  // Team choice and the team lab are one destination: the meeting.
  const destinations = $derived([
    {
      href: "/labs/",
      label: "Practice",
      icon: FlaskConical,
      within: ["/labs/", "/learn/"],
      live: false,
    },
    {
      href: $session?.membership.teamID ? "/lab/" : "/teams/",
      label: "Meeting",
      icon: Monitor,
      within: ["/teams/", "/lab/"],
      live: meetingOpen,
    },
    ...($session?.user.admin
      ? [
          {
            href: "/admin/",
            label: "Admin",
            icon: SlidersHorizontal,
            within: ["/admin/"],
            live: false,
          },
        ]
      : []),
  ]);
  // The coach console tab is a full-width workspace without the phone tab bar.
  const workspace = $derived(page.url.pathname.startsWith("/admin/terminal/"));
  function active(within: string[]) {
    return within.some((prefix) => page.url.pathname.startsWith(prefix));
  }
  const running = $derived(
    $adminView?.jobs.filter((j) => j.status === "running").length ?? 0,
  );
  const failures = $derived(
    $adminView?.jobs.filter((j) => j.status === "failed" && j.needsAttention)
      .length ?? 0,
  );
</script>

<svelte:head
  ><title>LabCentral</title><link
    rel="icon"
    type="image/svg+xml"
    href="/brand.svg?v=core"
  /><meta
    name="description"
    content="Guided practice labs and team meetings, in the cloud or on your own infrastructure."
  /></svelte:head
>
<a
  href="#main"
  class="sr-only focus:not-sr-only focus:absolute focus:z-40 focus:bg-base-100 focus:p-4"
  >Skip to content</a
>
<div
  class="min-h-screen bg-base-100 pr-[env(safe-area-inset-right)] pl-[env(safe-area-inset-left)] text-base-content"
>
  <header
    class="sticky top-0 z-30 border-b border-base-content/8 bg-base-100"
  >
    <div class="mx-auto flex h-14 max-w-6xl items-center gap-2 px-4 sm:px-6 lg:gap-8">
      <a
        href="/"
        aria-label="LabCentral home"
        class="flex min-h-11 shrink-0 items-center gap-2.5 rounded-field font-semibold tracking-tight focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary"
        ><Logo size={24} /><span>LabCentral</span></a
      >
      {#if $session}
        <nav
          aria-label="Main navigation"
          class={[
            "fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-base-content/10 bg-base-200 pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] lg:static lg:mr-auto lg:justify-start lg:gap-1 lg:border-0 lg:bg-transparent lg:p-0",
            workspace && "max-lg:hidden",
          ]}
        >
          {#each destinations as destination}
            <a
              href={destination.href}
              aria-current={active(destination.within) ? "page" : undefined}
              class={[
                "relative flex min-h-16 min-w-0 flex-1 flex-col items-center justify-center gap-1 text-xs focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary lg:min-h-9 lg:flex-none lg:flex-row lg:gap-2 lg:rounded-field lg:px-3 lg:text-sm",
                active(destination.within)
                  ? "text-primary lg:bg-base-200 lg:text-base-content"
                  : "text-base-content/70 hover:text-base-content",
              ]}
              ><destination.icon
                size={17}
                aria-hidden="true"
                class="lg:hidden"
              /><span>{destination.label}</span>{#if destination.live}<span
                  class="absolute top-3 ml-5 size-1.5 rounded-full bg-success lg:static lg:ml-0"
                  title="Meeting in progress"
                  aria-hidden="true"
                ></span>{/if}</a
            >
          {/each}
        </nav>
        {#if $session.user.admin && (running || failures)}<a
            href="/admin/#activity-title"
            class="btn btn-sm btn-outline ml-auto min-h-11 gap-2 px-2.5 text-xs font-normal lg:ml-0 lg:min-h-8"
            aria-label={`Live activity: ${running} running, ${failures} needing attention`}
            >{#if failures}<span class="size-2 rounded-full bg-error"></span
              >{failures} failed{:else}<span
                class="loading loading-ring loading-xs text-primary motion-reduce:animate-none"
              ></span>{running} running{/if}</a
          >{/if}
        <span
          class="hidden max-w-40 truncate text-sm text-base-content/60 xl:inline"
          >{$session.user.name}</span
        ><button
          onclick={logout}
          class={[
            "btn btn-sm btn-outline btn-square min-h-11 min-w-11 lg:min-h-9 lg:min-w-9",
            $session.user.admin && (running || failures) ? "" : "ml-auto lg:ml-0",
          ]}
          aria-label="Sign out"
          title="Sign out"><LogOut size={16} /></button
        >
      {:else if !$loading}<a
          class="btn btn-sm min-h-11 btn-outline ml-auto lg:min-h-9"
          href="/auth/login"
          data-sveltekit-reload>Sign in <ArrowUpRight size={16} /></a
        >{/if}
    </div>
  </header>
  <main
    id="main"
    class={workspace
      ? "w-full"
      : "mx-auto max-w-6xl px-4 pt-6 pb-[calc(6rem+env(safe-area-inset-bottom))] sm:px-6 lg:pt-10 lg:pb-16"}
  >
    <Notice message={error} dismissible />{@render children()}
  </main>
</div>
