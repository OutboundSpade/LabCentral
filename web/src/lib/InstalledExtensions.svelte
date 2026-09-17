<script lang="ts">
  import { onMount } from "svelte";
  import { Plug, RefreshCw, CircleAlert, ExternalLink } from "@lucide/svelte";
  import { api } from "./api";
  import type { components } from "./api.generated";
  import ExtensionPanels from "./ExtensionPanels.svelte";
  import SchemaField from "./SchemaField.svelte";
  import { initial, type Schema, type Value } from "./schema";
  type Plugin = components["schemas"]["ExtensionInventoryDTO"];
  const roles: Record<string, { name: string; description: string }> = {
    provider: {
      name: "Provider",
      description: "Creates and manages lab machines and networks.",
    },
    access: {
      name: "Console & network access",
      description: "Connects users to machines or lab networks.",
    },
    source: {
      name: "Template source",
      description: "Imports versioned lab templates from an external source.",
    },
    builder: {
      name: "Image builder",
      description: "Builds reusable machine images from lab recipes.",
    },
    browser: {
      name: "User interface",
      description: "Adds panels, actions or access views to LabCentral.",
    },
  };
  const names: Record<string, string> = {
    "io.labcentral.provider.proxmox": "Proxmox",
    "io.labcentral.provider.docker": "Docker",
    "io.labcentral.access.novnc": "noVNC",
  };
  let items = $state<Plugin[]>([]),
    drafts = $state<Record<string, Value>>({});
  let error = $state(""),
    loaded = $state(false),
    loading = $state(false);
  let refresh = async () => {};
  onMount(() => {
    const abort = new AbortController();
    refresh = async () => {
      if (loading) return;
      loading = true;
      try {
        const result = await api<{ items: Plugin[] }>("/admin/extensions", {
          signal: abort.signal,
          fresh: true,
        });
        if (!abort.signal.aborted) {
          items = result.items ?? [];
          loaded = true;
          error = "";
        }
      } catch (e) {
        if (!abort.signal.aborted)
          error =
            e instanceof Error
              ? e.message
              : "Could not load installed plugins.";
      } finally {
        loading = false;
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 10000);
    return () => {
      abort.abort();
      clearInterval(timer);
    };
  });
</script>

<section aria-labelledby="installed-plugins-title" class="space-y-5">
  <header class="flex flex-wrap items-start justify-between gap-3">
    <div class="max-w-2xl">
      <h2 id="installed-plugins-title" class="text-lg font-semibold">
        Installed plugins
      </h2>
      <p class="mt-2 text-sm text-base-content/70">
        Plugins add providers, console and network access, template sources,
        image builders, or interface features. Their roles appear below.
      </p>
    </div>
    <button
      type="button"
      class="btn btn-sm"
      disabled={loading}
      onclick={() => void refresh()}
      ><RefreshCw size={16} />Refresh plugins</button
    >
  </header>
  {#if error}<div role="alert" class="alert alert-error alert-soft">
      <CircleAlert size={20} /><span class="min-w-0 break-words">{error}</span>
    </div>{/if}
  {#if !loaded && loading}<p
      role="status"
      class="py-6 text-sm text-base-content/70"
    >
      Loading plugins…
    </p>
  {:else if loaded && !items.length && !error}<div
      class="rounded-box border border-base-300 p-6"
    >
      <Plug size={24} class="mb-3 text-base-content/60" />
      <h3 class="font-medium">No plugins installed</h3>
      <p class="mt-2 text-sm text-base-content/70">
        A server operator can install a plugin package and configure its
        service.
      </p>
    </div>
  {:else if items.length}<ul class="grid gap-4 lg:grid-cols-2">
      {#each items as item (item.instanceId)}{@const role = roles[
          item.category
        ] ?? {
          name: item.category,
          description: "Adds capabilities to LabCentral.",
        }}
        <li class="min-w-0 rounded-box border border-base-300 p-5">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <h3 class="min-w-0 break-all font-semibold">
              {names[item.pluginId] ?? item.pluginId}
            </h3>
            <span class="badge badge-sm" class:badge-error={!item.available}
              >{item.available ? "Available" : "Unavailable"}</span
            >
          </div>
          <p class="mt-2 text-sm font-medium">{role.name}</p>
          <p class="mt-1 text-sm text-base-content/70">{role.description}</p>
          {#if item.targetIds?.length}<p class="mt-3 break-all text-sm">
              Environments: {item.targetIds.join(", ")}
            </p>{/if}
          {#if item.methods?.length}<p class="mt-3 break-all text-sm">
              Access methods: {item.methods.join(", ")}
            </p>{/if}
          <details class="collapse collapse-arrow mt-4 border border-base-300">
            <summary class="collapse-title text-sm font-medium"
              >Technical details</summary
            >
            <div class="collapse-content space-y-2 text-sm">
              <dl class="space-y-2">
                <div>
                  <dt class="text-base-content/60">Plugin ID</dt>
                  <dd class="break-all font-mono text-xs">{item.pluginId}</dd>
                </div>
                <div>
                  <dt class="text-base-content/60">Service identity</dt>
                  <dd class="break-all font-mono text-xs">{item.instanceId}</dd>
                </div>
                <div>
                  <dt class="text-base-content/60">Version / contract</dt>
                  <dd>
                    {item.version} / {item.protocolMajor}.{item.protocolMinor}
                  </dd>
                </div>
              </dl>
              {#if item.permissions?.length}<p class="break-words">
                  Permissions: {item.permissions.join(", ")}
                </p>{/if}
              {#each item.bindings ?? [] as binding}<p class="break-all">
                  Binding {binding.id} · Workspaces: {(
                    binding.projectIds ?? []
                  ).join(", ")}{binding.hasCredentials
                    ? " · Credentials configured"
                    : ""}
                </p>{/each}
            </div>
          </details>
          {#if item.configurationSchema}{@const schema =
              item.configurationSchema as Schema}
            <details
              class="collapse collapse-arrow mt-3 border border-base-300"
            >
              <summary class="collapse-title text-sm font-medium"
                >Prepare configuration</summary
              >
              <div class="collapse-content">
                <p class="mb-3 text-sm text-base-content/70">
                  Prepare configuration for the server operator to validate,
                  install and activate with a service restart.
                </p>
                <SchemaField
                  {schema}
                  root={schema}
                  value={drafts[item.instanceId] ?? initial(schema, schema)}
                  onchange={(v) => (drafts[item.instanceId] = v)}
                  label="Configuration"
                />
                <pre
                  class="mt-3 max-w-full overflow-auto text-xs">{JSON.stringify(
                    drafts[item.instanceId] ?? initial(schema, schema),
                    null,
                    2,
                  )}</pre>
              </div>
            </details>{/if}
        </li>{/each}
    </ul>{/if}
  <details class="collapse collapse-arrow border border-base-300">
    <summary class="collapse-title font-medium"
      >Install or update a plugin</summary
    >
    <div class="collapse-content space-y-3 text-sm text-base-content/70">
      <p>
        A server operator installs the verified package, configures its
        permissions and service, then restarts the affected services. This
        applies to every plugin role.
      </p>
      <p>
        Provider plugins connect environments. Use the Environments tab to
        refresh discovery, configure limits and grant workspace access.
      </p>
      <a
        class="link inline-flex items-center gap-2"
        href="https://github.com/OutboundSpade/LabCentral/blob/main/docs/operations/plugin-installation.md"
        target="_blank"
        rel="noreferrer">Plugin installation guide <ExternalLink size={14} /></a
      >
    </div>
  </details>
</section>
<ExtensionPanels slot="plugin-settings" />
