<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "./api";
  import type { components } from "./api.generated";
  import GatewayClients from "./GatewayClients.svelte";
  import ExtensionPanels from "./ExtensionPanels.svelte";
  import SchemaField from "./SchemaField.svelte";
  import { initial, type Schema, type Value } from "./schema";
  let items = $state<components["schemas"]["ExtensionInventoryDTO"][]>([]);
  let drafts = $state<Record<string,Value>>({});
  let error = $state("");
  onMount(() => { const abort = new AbortController(); let loading=false;
    const refresh=async()=>{if(loading)return;loading=true;try{const r=await api<{items: components["schemas"]["ExtensionInventoryDTO"][]}>("/admin/extensions", {signal: abort.signal});if(!abort.signal.aborted){items=r.items;error="";}}catch{if(!abort.signal.aborted)error="Could not load installed extensions.";}finally{loading=false;}};
    void refresh();const timer=setInterval(()=>void refresh(),10000);return()=>{abort.abort();clearInterval(timer);};
  });
</script>
{#if error}<p role="alert" class="text-error">{error}</p>{/if}
{#if items.length}<section class="mt-6 rounded-box border border-base-300 p-5"><h2 class="font-semibold">Installed extensions</h2><ul class="mt-3 space-y-4">{#each items as item}<li class="border-b border-base-300 pb-4">
  <p>{item.pluginId} · {item.category} · {item.version} · {item.available ? "Available" : "Unavailable"}</p>
  <p class="text-sm">{item.instanceId} · Contract {item.protocolMajor}.{item.protocolMinor}</p>
  {#if item.permissions?.length}<p class="text-sm">Permissions: {item.permissions.join(", ")}</p>{/if}
  {#if item.targetIds?.length}<p class="text-sm">Targets: {item.targetIds.join(", ")}</p>{/if}
  {#if item.methods?.length}<p class="text-sm">Access methods: {item.methods.join(", ")}</p>{/if}
  {#each item.bindings??[] as binding}<p class="text-sm">Binding {binding.id} · Projects: {(binding.projectIds??[]).join(", ")}{binding.hasCredentials?" · Credentials configured":""}</p>{/each}
  {#if item.configurationSchema}{@const schema=item.configurationSchema as Schema}<details class="mt-3"><summary>Prepare binding configuration</summary>
    <p class="my-2 text-sm">Prepare configuration for the operator to validate and install. Applying it requires updating the installation file and restarting the affected service.</p>
    <SchemaField {schema} root={schema} value={drafts[item.instanceId]??initial(schema,schema)} onchange={v=>drafts[item.instanceId]=v} label="Configuration" />
    <pre class="mt-2 overflow-auto text-xs">{JSON.stringify(drafts[item.instanceId]??initial(schema,schema),null,2)}</pre>
  </details>{/if}
</li>{/each}</ul></section>{/if}
<ExtensionPanels slot="plugin-settings" />

<GatewayClients />
