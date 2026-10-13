<script lang="ts">
  import { page } from "$app/state";
  const sections = [
    { href: "/admin/", label: "Meeting" },
    { href: "/admin/templates/", label: "Templates" },
  ];
</script>

<nav aria-label="Administration" class="mb-8 overflow-x-auto">
  <div class="inline-flex gap-1 rounded-field bg-base-200 p-1">
    {#each sections as section}<a
        href={section.href}
        class={[
          "flex min-h-11 items-center rounded-[calc(var(--radius-field)-2px)] px-3.5 text-sm whitespace-nowrap focus-visible:outline-2 focus-visible:outline-primary lg:min-h-8",
          page.url.pathname === section.href
            ? "bg-base-300 font-medium text-base-content"
            : "text-base-content/70 hover:text-base-content",
        ]}
        aria-current={page.url.pathname === section.href ? "page" : undefined}
        >{section.label}</a
      >{/each}
  </div>
</nav>
