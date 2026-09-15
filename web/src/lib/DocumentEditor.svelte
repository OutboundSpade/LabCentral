<script lang="ts">
  import { api } from "./api";
  import SchemaField from "./SchemaField.svelte";
  import { initial, type Schema, type Value } from "./schema";
  let { source = $bindable("") }: { source?: string } = $props();
  let schema = $state<Schema>();
  let document = $state<Value>();
  let mode = $state<"source" | "form">("source");
  let busy = $state(false),
    error = $state("");
  let editedSource = "";
  $effect(() => {
    if (source !== editedSource) mode = "source";
  });
  async function form() {
    busy = true;
    error = "";
    try {
      schema ??= await api<Schema>("/lab-authoring-schema");
      document = source.trim()
        ? await api<Value>("/lab-authoring-documents", {
            method: "POST",
            body: JSON.stringify({ source }),
          })
        : initial(schema, schema);
      editedSource = source;
      mode = "form";
    } catch (e) {
      error = e instanceof Error ? e.message : "Could not build form.";
    } finally {
      busy = false;
    }
  }
  function update(value: Value) {
    document = value;
    editedSource = JSON.stringify(value, null, 2);
    source = editedSource;
  }
</script>

<div class="flex items-center gap-2">
  <span class="mr-auto text-sm font-medium">Lab definition</span><button
    type="button"
    class="btn btn-sm"
    aria-pressed={mode === "form"}
    disabled={busy}
    onclick={form}>Form</button
  ><button
    type="button"
    class="btn btn-sm"
    aria-pressed={mode === "source"}
    onclick={() => (mode = "source")}>Source</button
  >
</div>
{#if error}<p role="alert" class="text-sm text-error">{error}</p>{/if}
{#if mode === "form" && schema && document !== undefined}
  <p class="text-xs text-base-content/60">
    Fields come from the lab schema. Publishing validates the complete
    definition. Form edits use JSON, which is also valid YAML; source comments
    are not retained.
  </p>
  <SchemaField
    {schema}
    root={schema}
    value={document}
    onchange={update}
    label="Lab"
  />
{:else}<label class="grid gap-2 text-sm"
    >Lab YAML<textarea
      class="textarea h-80 w-full font-mono text-xs leading-6"
      bind:value={source}
      spellcheck="false"
      required></textarea></label
  >{/if}
