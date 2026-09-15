<script lang="ts">
  import { page } from '$app/state';
  import Console from '$lib/Console.svelte';
</script>

<Console accessSessionId={page.params.id ?? ''} />
