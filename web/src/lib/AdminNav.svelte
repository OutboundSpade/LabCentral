<script lang="ts">
  import { page } from "$app/state";
  const sections = [
    { href: "/admin/", label: "Meeting", nested: false },
    { href: "/admin/templates/", label: "Templates", nested: true },
  ];
  const current = (s: (typeof sections)[number]) =>
    s.nested ? page.url.pathname.startsWith(s.href) : page.url.pathname === s.href;
</script>

<nav aria-label="Administration" class="mb-8 overflow-x-auto">
  <div class="inline-flex gap-1 rounded-field bg-base-200 p-1">
    {#each sections as section}<a
        href={section.href}
        class={[
          "flex min-h-11 items-center rounded-[calc(var(--radius-field)-2px)] px-3.5 text-sm whitespace-nowrap focus-visible:outline-2 focus-visible:outline-primary lg:min-h-8",
          current(section)
            ? "bg-base-300 font-medium text-base-content"
            : "text-base-content/70 hover:text-base-content",
        ]}
        aria-current={current(section) ? "page" : undefined}
        >{section.label}</a
      >{/each}
  </div>
</nav>
