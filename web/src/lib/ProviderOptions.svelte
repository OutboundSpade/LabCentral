<script lang="ts">
  import type { ProviderTarget } from "./api";
  import SchemaField from "./SchemaField.svelte";
  import { discoveryJSON, initial, type Value } from "./schema";
  let {
    target,
    source = $bindable(""),
  }: { target?: ProviderTarget; source?: string } = $props();
  const schema = $derived(
    discoveryJSON(target?.discovery, "override_json_schema"),
  );
  const value = $derived.by(() => {
    try {
      return JSON.parse(source) as Value;
    } catch {
      return undefined;
    }
  });
  let raw = $state(false);
</script>

<section class="mt-5 grid gap-3">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h3 class="text-sm font-medium">Plugin options</h3>
    {#if !source}<button
        type="button"
        class="btn btn-sm btn-ghost"
        onclick={() => {
          source = JSON.stringify(schema ? initial(schema, schema) : {});
          raw = !schema;
        }}>Customize options</button
      >{:else}<button
        type="button"
        class="btn btn-sm btn-ghost"
        onclick={() => (source = "")}>Use template defaults</button
      >{/if}
  </div>
  {#if source}{#if schema && value !== undefined && !raw}<SchemaField
        {schema}
        root={schema}
        {value}
        onchange={(v) => (source = JSON.stringify(v))}
        label="Provider override"
      />{:else}<label class="grid gap-2 text-sm"
        >Provider override JSON<textarea
          class="textarea min-h-28 w-full font-mono text-xs"
          bind:value={source}
          oninput={(e) => {
            try {
              JSON.parse(e.currentTarget.value);
              e.currentTarget.setCustomValidity("");
            } catch {
              e.currentTarget.setCustomValidity("Enter valid JSON.");
            }
          }}></textarea></label
      >{/if}{#if schema}<button
        type="button"
        class="btn btn-xs btn-ghost justify-self-start"
        onclick={() => (raw = !raw)}>{raw ? "Use form" : "Edit JSON"}</button
      >{/if}{:else}<p class="text-xs text-base-content/60">
      Using template and environment defaults.
    </p>{/if}
</section>
