<script lang="ts">
  import { page } from "$app/state";
  import { templateName } from "$lib/display";
  import Guard from "$lib/Guard.svelte";
  import AdminNav from "$lib/AdminNav.svelte";
  import PollStatus from "$lib/PollStatus.svelte";
  import TemplateExplorer from "$lib/TemplateExplorer.svelte";
  import { session } from "$lib/api";
  // /admin/templates/<name>/ opens the newest revision (the link to share);
  // /admin/templates/<name>/<revision>/ pins one exact revision.
  const name = $derived(page.params.name ?? "");
</script>

<svelte:head><title>{templateName(name)} · Templates · LabCentral</title></svelte:head>
{#if $session?.user.admin}<AdminNav />{/if}
<Guard admin><PollStatus /><TemplateExplorer {name} revision={page.params.revision ?? ""} /></Guard>
