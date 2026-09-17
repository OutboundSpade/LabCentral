<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "./api";
  import ExtensionFrame from "./ExtensionFrame.svelte";
  import type { components } from "./api.generated";
  let { instanceId = "", resourceId = "", slot = "lab-panel" }: { instanceId?: string; resourceId?:string; slot?: string } = $props();
  let extensions = $state<components["schemas"]["BrowserExtensionDTO"][]>([]);
  onMount(() => {
    const abort = new AbortController();
    let loading = false;
    const refresh = async () => { if (loading) return; loading = true;
    await api<{ items: components["schemas"]["BrowserExtensionDTO"][] }>("/extensions/browser", { signal: abort.signal }).then(result => { if (!abort.signal.aborted) extensions = result.items; }).catch(() => { extensions = []; });
    loading = false; };
    void refresh();
    const timer = setInterval(() => void refresh(), 10000);
    return () => { clearInterval(timer); abort.abort(); };
  });
</script>
{#each extensions.filter(e => e.slot === slot) as extension (`${extension.pluginInstance}/${extension.id}/${extension.url}/${instanceId}/${resourceId}`)}
  <section class="mt-6"><h3 class="mb-3 font-semibold">{extension.title}</h3><ExtensionFrame {extension} {instanceId} {resourceId} /></section>
{/each}
