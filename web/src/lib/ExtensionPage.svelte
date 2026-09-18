<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "./api";
  import ExtensionFrame from "./ExtensionFrame.svelte";
  import type { components } from "./api.generated";
  let { contribution, projectId }: {contribution: string; projectId: string} = $props();
  let extensions = $state<components["schemas"]["BrowserExtensionDTO"][]>([]);
  let loading = $state(true);
  let error = $state("");
  let selected = $derived(extensions.find(e => e.slot === "page" && `${e.pluginInstance}/${e.id}` === contribution));
  onMount(() => {
    const abort = new AbortController();
    let pending = false;
    const refresh = async () => {
      if (pending) return;
      pending = true;
      try {
        const result = await api<{items: typeof extensions}>("/extensions/browser", {signal: abort.signal});
        if (!abort.signal.aborted) {extensions = result.items; error = "";}
      } catch {
        if (!abort.signal.aborted) {extensions = []; error = "The extension could not be loaded.";}
      } finally {pending = false; loading = false;}
    };
    void refresh();
    const interval = setInterval(() => void refresh(), 10000);
    return () => {abort.abort(); clearInterval(interval);};
  });
</script>
{#if loading}<p role="status">Loading extension…</p>
{:else if error}<p role="alert">{error}</p>
{:else if !selected}<p role="status">This feature is not installed or is unavailable.</p>
{:else if !projectId}<p role="status">Select a workspace to use this feature.</p>
{:else}{#key `${selected.pluginInstance}/${selected.id}/${selected.url}/${projectId}`}
  <h2 class="mb-4 text-xl font-semibold">{selected.title}</h2>
  <ExtensionFrame extension={selected} {projectId} />
{/key}{/if}
