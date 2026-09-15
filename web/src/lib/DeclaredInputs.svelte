<script lang="ts">
  import type { InputValue, TemplateParameter } from "./api";
  let {
    parameters,
    values = $bindable<InputValue[]>([]),
    valid = $bindable(true),
  }: {
    parameters: TemplateParameter[];
    values?: InputValue[];
    valid?: boolean;
  } = $props();
  let entered = $state<Record<string, string>>({});
  let error = $state("");
  function options(parameter: TemplateParameter): unknown[] { return parameter.type === "boolean" ? ["true", "false"] : (parameter.enum ?? []); }
  $effect(() => {
    // A keyed parent resets this form when the selected revision changes.
    const next: InputValue[] = [];
    let message = "";
    for (const parameter of parameters) {
      if (parameter.type === "secretRef") {
        if (parameter.required) message = "This lab requires an administrator-managed secret reference. Contact your instructor.";
        continue;
      }
      const raw = entered[parameter.name];
      const value = raw === undefined ? undefined : String(raw);
      if (value === undefined || value === "") {
        if (parameter.required && parameter.default === undefined)
          message = "Complete the required lab options.";
        continue;
      }
      const base = {
        name: parameter.name,
        kind: parameter.type as InputValue["kind"],
      };
      if (parameter.type === "integer" || parameter.type === "number") {
        const number = Number(value);
        if (
          !Number.isFinite(number) ||
          (parameter.type === "integer" && !Number.isSafeInteger(number))
        )
          message = "Enter a valid number.";
        if ((parameter.min !== undefined && number < parameter.min) ||
            (parameter.max !== undefined && number > parameter.max)) message = "Enter a number within the allowed range.";
        next.push(
          parameter.type === "integer"
            ? { ...base, integer: number }
            : { ...base, number },
        );
      } else if (parameter.type === "enum") {
        const option = parameter.enum?.find((item) => String(item) === value);
        if (option === undefined) message = "Choose a listed option.";
        next.push(typeof option === "number" ? { ...base, integer: option } : { ...base, string: value });
      } else if (parameter.type === "boolean")
        next.push({ ...base, boolean: value === "true" });
      else if (parameter.type === "stringList")
        next.push({ ...base, stringList: value.split("\n") });
      else next.push({ ...base, string: value });
    }
    error = message;
    valid = !message;
    values = next;
  });
</script>

{#if parameters.length}
  <fieldset class="fieldset gap-3">
    <legend class="fieldset-legend text-base">Lab options</legend>
    {#each parameters as parameter}
      <label class="grid gap-2 text-sm">
        <span
          >{parameter.description || parameter.name}{parameter.required
            ? " *"
            : ""}</span
        >
        {#if parameter.type === "secretRef"}
          <p class="text-base-content/70">Managed by your administrator. Do not enter a secret here.</p>
        {:else if parameter.type === "boolean" || parameter.type === "enum"}
          <select
            class="select w-full"
            bind:value={entered[parameter.name]}
            required={parameter.required && parameter.default === undefined}
          >
            <option value=""
              >{parameter.default === undefined
                ? "Choose an option"
                : `Default: ${parameter.default}`}</option
            >
            {#each options(parameter) as option}<option
                value={String(option)}>{String(option)}</option
              >{/each}
          </select>
        {:else if parameter.type === "stringList"}
          <textarea
            class="textarea w-full"
            bind:value={entered[parameter.name]}
            placeholder="One value per line"></textarea>
        {:else if parameter.type === "number" || parameter.type === "integer"}
          <input
            class="input w-full"
            type="number"
            min={parameter.min}
            max={parameter.max}
            step={parameter.type === "integer" ? 1 : "any"}
            bind:value={entered[parameter.name]}
            placeholder={parameter.default === undefined
              ? ""
              : String(parameter.default)}
          />
        {:else}<input
            class="input w-full"
            bind:value={entered[parameter.name]}
            pattern={parameter.pattern}
            placeholder={parameter.default === undefined
              ? ""
              : String(parameter.default)}
          />{/if}
      </label>
    {/each}
    {#if error}<p class="text-sm text-base-content/70" aria-live="polite">
        {error}
      </p>{/if}
  </fieldset>
{/if}
