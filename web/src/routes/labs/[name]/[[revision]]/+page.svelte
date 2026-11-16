<script lang="ts">
  import { page } from "$app/state";
  import PracticeLibrary from "$lib/PracticeLibrary.svelte";
  // /labs/<name>/ opens the newest revision; /labs/<name>/<revision>/ pins one.
</script>

<PracticeLibrary name={page.params.name ?? ""} revision={page.params.revision ?? ""} />
