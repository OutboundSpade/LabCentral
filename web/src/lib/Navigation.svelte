<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { api } from "./api";
  import type { components } from "./api.generated";
  let featurePages = $state<components["schemas"]["BrowserExtensionDTO"][]>([]);
  onMount(() => {
    const abort = new AbortController();
    let pending = false;
    const refresh = async () => {
      if (pending) return;
      pending = true;
      try {
        const result = await api<{ items: typeof featurePages }>(
          "/extensions/browser",
          { signal: abort.signal },
        );
        if (!abort.signal.aborted)
          featurePages = result.items.filter((e) => e.slot === "page");
      } catch {
        if (!abort.signal.aborted) featurePages = [];
      } finally {
        pending = false;
      }
    };
    void refresh();
    const interval = setInterval(() => void refresh(), 10000);
    return () => {
      abort.abort();
      clearInterval(interval);
    };
  });
  import {
    LayoutDashboard,
    BookOpen,
    Users,
    ShieldCheck,
    Server,
    Settings,
    FolderOpen,
    Plug,
  } from "@lucide/svelte";
  import type { View } from "./navigation";
  let {
    view,
    author,
    manager,
    administrator,
    authenticated,
  }: {
    view: View;
    author?: boolean;
    manager: boolean;
    administrator: boolean;
    authenticated: boolean;
  } = $props();
  let groups = $derived([
    {
      title: "LABS",
      items: [
        { view: "labs", href: "/", label: "My labs", icon: LayoutDashboard },
        ...(manager
          ? [
              {
                view: "settings",
                href: "/settings/",
                label: "Settings",
                icon: Settings,
              },
            ]
          : []),
      ],
    },
    {
      title: "ADMINISTRATION",
      items: administrator
        ? [
            ...(authenticated
              ? [
                  {
                    view: "workspaces",
                    href: "/workspaces/",
                    label: "Workspaces",
                    icon: FolderOpen,
                  },
                  {
                    view: "people",
                    href: "/people/",
                    label: "People",
                    icon: ShieldCheck,
                  },
                ]
              : []),
            {
              view: "plugins",
              href: "/plugins/",
              label: "Integrations",
              icon: Plug,
            },
            {
              view: "operations",
              href: "/operations/",
              label: "Operations",
              icon: Server,
            },
          ]
        : [],
    },
  ]);
</script>

<nav aria-label="Main navigation">
  <ul class="menu w-full gap-1">
    {#if featurePages.length}
      <li class="menu-title mt-4 text-xs">FEATURES</li>
      {#each featurePages as feature (`${feature.pluginInstance}/${feature.id}`)}
        {@const active = feature.routeAlias
          ? view === feature.routeAlias
          : view === "feature" &&
            page.url.searchParams.get("contribution") ===
              `${feature.pluginInstance}/${feature.id}`}
        <li>
          <a
            href={feature.routeAlias
              ? `/${feature.routeAlias}/`
              : `/feature/?contribution=${encodeURIComponent(`${feature.pluginInstance}/${feature.id}`)}`}
            class:menu-active={active}
            aria-current={active ? "page" : undefined}
            ><Plug size={18} />{feature.title}</a
          >
        </li>
      {/each}
    {/if}
    {#each groups as group}
      {#if group.items.length}<li class="menu-title mt-4 text-xs">
          {group.title}
        </li>{/if}
      {#each group.items as item}
        {@const active =
          view === item.view ||
          (["detail", "launch"].includes(view) && item.view === "labs")}
        <li>
          <a
            href={item.href}
            class:menu-active={active}
            aria-current={active ? "page" : undefined}
            ><item.icon size={18} />{item.label}</a
          >
        </li>
      {/each}
    {/each}
  </ul>
</nav>
