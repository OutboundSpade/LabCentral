<script lang="ts">
  import { page } from "$app/state";
  import { SlidersHorizontal, BookOpen, GitBranch } from "@lucide/svelte";
  // Phones show the short label so all three sections fit without scrolling.
  const sections = [
    {
      href: "/admin/",
      label: "Manage meeting",
      short: "Meeting",
      icon: SlidersHorizontal,
    },
    {
      href: "/admin/labs/",
      label: "Lab publishing",
      short: "Publishing",
      icon: BookOpen,
    },
    {
      href: "/admin/templates/",
      label: "Templates",
      short: "Templates",
      icon: GitBranch,
    },
  ];
</script>

<nav aria-label="Administration" class="mb-5 overflow-x-auto lg:mb-6">
  <div class="tabs tabs-border flex-nowrap">
    {#each sections as section}<a
        href={section.href}
        class={[
          "tab min-h-11 flex-nowrap gap-1.5 whitespace-nowrap text-sm max-sm:[--tab-p:0.5rem] sm:gap-2",
          page.url.pathname === section.href ? "tab-active text-primary" : "",
        ]}
        aria-current={page.url.pathname === section.href ? "page" : undefined}
        ><section.icon size={15} aria-hidden="true" /><span class="sm:hidden"
          >{section.short}</span
        ><span class="hidden sm:inline">{section.label}</span></a
      >{/each}
  </div>
</nav>
