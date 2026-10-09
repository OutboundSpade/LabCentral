<script lang="ts">
  import "../app.css";
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import {
    Monitor,
    Users,
    SlidersHorizontal,
    LogOut,
    ArrowUpRight,
    FlaskConical,
    Activity,
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
      location.assign("/");
    } catch (e) {
      error = (e as Error).message;
      await refresh();
    }
  }
  const destinations = $derived([
    {
      href: "/labs/",
      label: "Labs",
      icon: FlaskConical,
      within: ["/labs/", "/learn/"],
    },
    { href: "/teams/", label: "Teams", icon: Users, within: ["/teams/"] },
    { href: "/lab/", label: "Your lab", icon: Monitor, within: ["/lab/"] },
    ...($session?.user.admin
      ? [
          {
            href: "/admin/",
            label: "Admin",
            icon: SlidersHorizontal,
            within: ["/admin/"],
          },
        ]
      : []),
  ]);
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
    href="/brand.svg?v=stage-rail"
  /><meta
    name="description"
    content="Guided practice labs and CDC team meetings, in the cloud or on your own infrastructure."
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
  <header class="border-b border-base-content/10">
    <div
      class="navbar mx-auto min-h-14 max-w-7xl gap-2 px-4 sm:px-6 lg:gap-6 lg:px-8"
    >
      <a
        href="/"
        aria-label="LabCentral home"
        class="flex min-h-11 shrink-0 items-center gap-2.5 rounded-field text-base font-semibold tracking-tight focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary"
        ><Logo size={26} /><span>LabCentral</span></a
      >
      {#if $session}
        <nav
          aria-label="Main navigation"
          class="fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-base-content/10 bg-base-200 pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] lg:static lg:mr-auto lg:justify-start lg:gap-1 lg:border-0 lg:bg-transparent lg:p-0"
        >
          {#each destinations as destination}
            <a
              href={destination.href}
              aria-current={active(destination.within) ? "page" : undefined}
              title={destination.href === "/lab/" &&
              !$session.membership.teamID
                ? "Join a team to access your meeting lab"
                : undefined}
              class={[
                "flex min-h-16 min-w-0 flex-1 flex-col items-center justify-center gap-1 border-t-2 px-3 text-xs hover:text-base-content focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary lg:min-h-14 lg:flex-none lg:flex-row lg:gap-2 lg:border-t-0 lg:border-b-2 lg:text-sm",
                active(destination.within)
                  ? "border-primary text-primary"
                  : "border-transparent text-base-content/75",
              ]}
              ><destination.icon size={17} aria-hidden="true" /><span
                class="max-w-20 truncate text-center lg:max-w-none lg:whitespace-nowrap"
                >{destination.label}</span
              ></a
            >
          {/each}
        </nav>
        {#if $session.user.admin}<a
            href="/admin/#activity-title"
            class="btn btn-sm btn-ghost ml-auto min-h-11 gap-1.5 px-2 text-xs lg:ml-0"
            aria-label={`Live activity: ${running} running, ${failures} needing attention`}
            ><Activity size={16} /><span class="xl:hidden">Activity</span><span
              class="hidden xl:inline"
              >{running} running · {failures} failed</span
            >{#if failures}<span
                class="badge badge-sm badge-soft badge-error xl:hidden"
                >{failures}</span
              >{/if}</a
          >{/if}
        <span
          class="ml-auto hidden max-w-36 truncate text-xs text-base-content/75 2xl:inline lg:ml-0"
          >{$session.user.name}</span
        ><button
          onclick={logout}
          class="btn btn-sm btn-ghost ml-auto min-h-11 min-w-11 lg:ml-0"
          aria-label="Sign out"
          title="Sign out"><LogOut size={17} /></button
        >
      {:else if !$loading}<a
          class="btn btn-sm min-h-11 btn-ghost ml-auto"
          href="/auth/login"
          data-sveltekit-reload>Sign in <ArrowUpRight size={16} /></a
        >{/if}
    </div>
  </header>
  <main
    id="main"
    class="mx-auto max-w-7xl px-4 pt-5 pb-[calc(6rem+env(safe-area-inset-bottom))] sm:px-6 lg:px-8 lg:py-8"
  >
    <Notice message={error} dismissible />{@render children()}
  </main>
</div>
