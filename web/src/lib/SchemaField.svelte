<script lang="ts">
  import SchemaField from "./SchemaField.svelte";
  import { resolve, initial, type Schema, type Value } from "./schema";
  let {
    schema,
    root,
    value,
    onchange,
    label = "",
    required = false,
    depth = 0,
  }: {
    schema: Schema;
    root: Schema;
    value: Value;
    onchange: (v: Value) => void;
    label?: string;
    required?: boolean;
    depth?: number;
  } = $props();
  const s = $derived(resolve(schema, root));
  const object = $derived(
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, Value>)
      : {},
  );
  const properties = $derived(
    Object.entries(s.properties ?? {}).slice(0, 128) as [string, Schema][],
  );
  const fallback = $derived(
    depth > 12 ||
      s.oneOf ||
      s.anyOf ||
      s.allOf ||
      (!s.type && !s.enum && s.const === undefined) ||
      Array.isArray(s.type) ||
      (s.type === "object" && !properties.length),
  );
  let rawError = $state("");
  function set(key: string, next: Value | undefined) {
    const result = { ...object };
    if (next === undefined) delete result[key];
    else
      Object.defineProperty(result, key, {
        value: next,
        enumerable: true,
        writable: true,
        configurable: true,
      });
    onchange(result);
  }
</script>

{#if fallback}
  <label class="grid gap-2 text-sm"
    >{label} (JSON)<textarea
      class="textarea min-h-24 w-full font-mono text-xs"
      value={JSON.stringify(value, null, 2)}
      oninput={(e) => {
        try {
          onchange(JSON.parse(e.currentTarget.value));
          rawError = "";
          e.currentTarget.setCustomValidity("");
        } catch {
          rawError = "Enter valid JSON.";
          e.currentTarget.setCustomValidity(rawError);
        }
      }}></textarea></label
  >
  {#if rawError}<p role="alert" class="text-sm text-error">{rawError}</p>{/if}
{:else if s.type === "object"}
  <fieldset class="min-w-0 space-y-4 rounded-box border border-base-300 p-4">
    <legend class="px-1 text-sm font-semibold">{label}</legend>
    {#each properties as [key, child] (key)}
      {@const needed = s.required?.includes(key) ?? false}
      {#if needed || Object.hasOwn(object, key)}
        <div class="grid gap-2">
          <SchemaField
            schema={child}
            {root}
            value={object[key]}
            onchange={(v) => set(key, v)}
            label={key}
            required={needed}
            depth={depth + 1}
          />{#if !needed}<button
              type="button"
              class="btn btn-xs btn-ghost justify-self-start"
              onclick={() => set(key, undefined)}>Remove {key}</button
            >{/if}
        </div>
      {:else}<button
          type="button"
          class="btn btn-sm btn-ghost mr-2"
          onclick={() => set(key, initial(child, root))}>Add {key}</button
        >{/if}
    {/each}
    {#if Object.keys(object).some((k) => !properties.some(([key]) => key === k))}<p
        class="text-xs text-base-content/60"
      >
        Additional fields are preserved. Use source mode to edit them.
      </p>{/if}
  </fieldset>
{:else if s.type === "array"}
  <fieldset class="min-w-0 space-y-3 rounded-box border border-base-300 p-4">
    <legend class="px-1 text-sm font-semibold">{label}</legend>
    {#each Array.isArray(value) ? value.slice(0, 256) : [] as item, i}<div
        class="grid gap-2"
      >
        <SchemaField
          schema={s.items ?? {}}
          {root}
          value={item}
          onchange={(v) =>
            onchange((value as Value[]).map((a, j) => (j === i ? v : a)))}
          label={`${label} ${i + 1}`}
          depth={depth + 1}
        /><button
          type="button"
          class="btn btn-xs btn-ghost justify-self-start"
          onclick={() => onchange((value as Value[]).filter((_, j) => j !== i))}
          >Remove item {i + 1}</button
        >
      </div>{/each}
    <button
      type="button"
      class="btn btn-sm"
      disabled={Array.isArray(value) &&
        value.length >= Math.min(s.maxItems ?? 256, 256)}
      onclick={() =>
        onchange([
          ...(Array.isArray(value) ? value : []),
          initial(s.items ?? {}, root),
        ])}>Add {label} item</button
    >
  </fieldset>
{:else if s.const !== undefined}
  <p class="text-sm">
    <span class="text-base-content/60">{label}</span>
    {String(s.const)}
  </p>
{:else if s.enum}
  <label class="grid gap-2 text-sm"
    >{label}{required ? " *" : ""}<select
      class="select w-full"
      {required}
      value={JSON.stringify(value)}
      onchange={(e) => onchange(JSON.parse(e.currentTarget.value))}
      >{#each s.enum as option}<option value={JSON.stringify(option)}
          >{String(option)}</option
        >{/each}</select
    ></label
  >
{:else if s.type === "boolean"}
  <label class="flex items-center gap-2 text-sm"
    ><input
      type="checkbox"
      class="checkbox checkbox-sm"
      checked={value === true}
      onchange={(e) => onchange(e.currentTarget.checked)}
    />{label}</label
  >
{:else if s.type === "number" || s.type === "integer"}
  <label class="grid gap-2 text-sm"
    >{label}{required ? " *" : ""}<input
      class="input w-full"
      type="number"
      {required}
      min={s.minimum}
      max={s.maximum}
      step={s.type === "integer" ? 1 : "any"}
      value={typeof value === "number" ? value : ""}
      oninput={(e) =>
        onchange(
          e.currentTarget.value === "" ? null : e.currentTarget.valueAsNumber,
        )}
    /></label
  >
{:else}
  <label class="grid gap-2 text-sm"
    >{label}{required ? " *" : ""}<input
      class="input w-full"
      type={s.writeOnly ? "password" : "text"}
      autocomplete="off"
      {required}
      minlength={s.minLength}
      maxlength={s.maxLength}
      value={typeof value === "string" ? value : ""}
      oninput={(e) => onchange(e.currentTarget.value)}
    /></label
  >
{/if}
{#if s.description}<p class="text-xs text-base-content/60">
    {s.description}
  </p>{/if}
