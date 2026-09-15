<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { QueryClientProvider } from '@tanstack/svelte-query';
  import { page } from '$app/state';
  import Workspace from '$lib/Workspace.svelte';
  import { queryClient, listenForIdentityChanges } from '$lib/query';
  onMount(listenForIdentityChanges);
  let { children } = $props();
</script>

<QueryClientProvider client={queryClient}>
  {#if page.url.pathname.startsWith('/access/') || page.error}
    {@render children()}
  {:else}
    <Workspace />
  {/if}
</QueryClientProvider>
