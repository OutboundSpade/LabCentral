<script lang="ts">
  import Guard from "$lib/Guard.svelte";
  import CoachTerminal from "$lib/CoachTerminal.svelte";
  import { session } from "$lib/api";
</script>

<!-- The console revalidates identity itself; Guard only covers signed-out and student visits. -->
<div class={$session?.user.admin ? "" : "px-4 pt-6 sm:px-6 lg:pt-10"}>
  <Guard admin><CoachTerminal /></Guard>
</div>
