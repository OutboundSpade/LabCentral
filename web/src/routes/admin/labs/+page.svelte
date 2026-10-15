<script lang="ts">
  import { goto } from "$app/navigation";
  import { ArrowRight } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import { session } from "$lib/api";
  // Published labs were replaced by the template library. Saved publications and
  // progress are retained; learners keep their existing lesson links.
  $effect(() => {
    if ($session?.user.admin)
      void goto("/admin/templates/", { replaceState: true });
  });
</script>

<svelte:head><title>Lab templates · LabCentral</title></svelte:head>
<Guard admin>
  <p class="flex min-h-48 flex-wrap items-center gap-3 text-sm text-base-content/70" role="status">
    <span class="loading loading-spinner loading-sm"></span>Practice now starts from
    templates. Opening Lab templates…
    <a class="btn btn-sm btn-outline min-h-11" href="/admin/templates/"
      >Open Lab templates<ArrowRight size={15} /></a
    >
  </p>
</Guard>
