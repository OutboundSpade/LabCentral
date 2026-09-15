<script lang="ts">
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
        {
          view: "classroom",
          href: "/classroom/",
          label: "Classroom",
          icon: Users,
        },
        ...(author
          ? [
              {
                view: "templates",
                href: "/templates/",
                label: "Templates",
                icon: BookOpen,
              },
            ]
          : []),
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
              label: "Plugins",
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
    {#each groups as group}
      {#if group.items.length}<li class="menu-title mt-4 text-xs">
          {group.title}
        </li>{/if}
      {#each group.items as item}
        {@const active =
          view === item.view || (["detail", "launch"].includes(view) && item.view === "labs")}
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
