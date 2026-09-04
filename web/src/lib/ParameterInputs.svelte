<script lang="ts">
  import type { InputValue } from './api';
  let { values = $bindable<InputValue[]>([]), valid = $bindable(true) } = $props();
  type Row = { name: string; kind: InputValue['kind']; value: string };
  let rows = $state<Row[]>([]);
  let error = $state('');
  const kinds: InputValue['kind'][] = ['string', 'integer', 'number', 'boolean', 'enum', 'stringList', 'cidr', 'ip', 'duration'];
  function update() {
    error = '';
    valid = false;
    try {
      values = rows.map((row) => {
        if (!/^[a-z][a-z0-9_]{0,62}$/.test(row.name) || rows.filter((r) => r.name === row.name).length !== 1) throw new Error('Use unique declared parameter names.');
        const base = { name: row.name, kind: row.kind };
        if (row.kind === 'boolean') return { ...base, boolean: row.value === 'true' };
        if (row.kind === 'integer' || row.kind === 'number') {
          const number = Number(row.value);
          if (!row.value.trim() || !Number.isFinite(number) || (row.kind === 'integer' && !Number.isSafeInteger(number))) throw new Error('Enter a valid numeric value.');
          return row.kind === 'integer' ? { ...base, integer: number } : { ...base, number };
        }
        if (row.kind === 'stringList') return { ...base, stringList: row.value === '' ? [] : row.value.split('\n') };
        return { ...base, string: row.value };
      });
      valid = true;
    } catch (e) { error = e instanceof Error ? e.message : 'Invalid input'; values = []; }
  }
</script>
<details class="rounded-lg border border-base-300 p-3">
  <summary class="cursor-pointer text-sm font-medium">Lab parameters</summary>
  <p class="my-2 text-xs text-base-content/70">Use the parameter names declared in the lab. Lists take one value per line.</p>
  {#each rows as row, i}
    <div class="my-2 flex flex-wrap items-start gap-2">
      <input class="input input-sm" aria-label="Parameter name" placeholder="Parameter name" bind:value={row.name} oninput={update} />
      <select class="select select-sm" aria-label="Parameter type" bind:value={row.kind} onchange={update}>{#each kinds as kind}<option value={kind}>{kind}</option>{/each}</select>
      {#if row.kind === 'boolean'}
        <select class="select select-sm" aria-label="Parameter value" bind:value={row.value} onchange={update}><option value="false">False</option><option value="true">True</option></select>
      {:else}<textarea class="textarea textarea-sm" aria-label="Parameter value" placeholder="Value" bind:value={row.value} oninput={update}></textarea>{/if}
      <button type="button" class="btn btn-sm btn-ghost" onclick={() => { rows = rows.filter((_, index) => index !== i); update(); }}>Remove</button>
    </div>
  {/each}
  {#if error}<p role="alert" class="text-sm text-error">{error}</p>{/if}
  <button type="button" class="btn btn-sm btn-ghost" onclick={() => { rows = [...rows, { name: '', kind: 'string', value: '' }]; update(); }}>Add parameter</button>
</details>
