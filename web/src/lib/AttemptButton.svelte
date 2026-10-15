<script lang="ts">
  import type { Component } from "svelte";
  import { onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { RotateCw, TriangleAlert } from "@lucide/svelte";
  import { session, refresh } from "./api";
  import { sessionScope, learnPath } from "./learning";
  import { createAttempt, attemptPending } from "./library";
  let {
    template,
    scenarioID = "",
    label,
    name,
    icon: Icon,
    primary = false,
    blocked = "",
    describedby,
    class: extra = "",
  }: {
    template: string;
    scenarioID?: string;
    label: string;
    name?: string;
    icon?: Component<{ size?: number }>;
    primary?: boolean;
    blocked?: string;
    describedby?: string;
    class?: string;
  } = $props();
  let busy = $state(false);
  let error = $state("");
  let unknown = $state(false);
  let alive = true;
  onDestroy(() => (alive = false));
  const id = $props.id();
  // Clear outcomes when the target or identity changes; late responses are ignored.
  const target = $derived(`${sessionScope($session)}|${template}|${scenarioID}`);
  $effect(() => {
    void target;
    error = "";
    unknown = attemptPending(template, scenarioID);
  });
  async function create() {
    if (busy || blocked) return;
    const requested = target;
    busy = true;
    error = "";
    const result = await createAttempt(template, scenarioID);
    if (!alive || requested !== target || result.scope !== sessionScope($session)) {
      busy = false;
      return;
    }
    if (result.ok) {
      await goto(learnPath(result.lab.id));
      busy = false;
      return;
    }
    busy = false;
    error = result.message;
    unknown = result.unknown;
    if ([401, 403].includes(result.status)) void refresh();
  }
</script>

<button
  type="button"
  class={["btn btn-sm min-h-11 lg:min-h-8", primary ? "btn-primary" : "", extra]}
  disabled={busy || !!blocked}
  aria-busy={busy}
  aria-label={name ? `${unknown ? "Retry: " : ""}${name}` : undefined}
  aria-describedby={[describedby, error ? `${id}-error` : unknown && !blocked ? `${id}-pending` : ""]
    .filter(Boolean)
    .join(" ") || undefined}
  onclick={create}
  >{#if busy}<span class="loading loading-spinner loading-xs"></span>{:else if unknown}<RotateCw
      size={15}
    />{:else if Icon}<Icon size={15} />{/if}{unknown ? `Retry ${label.toLowerCase()}` : label}</button
>
{#if unknown && !error && !blocked}<p
    id={`${id}-pending`}
    class="mt-2 basis-full text-xs leading-relaxed text-warning"
    role="status"
  >
    An earlier start wasn't confirmed. Retrying repeats that request without creating a duplicate.
  </p>{/if}
{#if error}<p
    id={`${id}-error`}
    class="mt-2 flex basis-full items-start gap-2 text-sm leading-relaxed text-error"
    role="alert"
  >
    <TriangleAlert size={15} class="mt-0.5 shrink-0" aria-hidden="true" /><span
      class="min-w-0 break-words">{error}</span
    >
  </p>{/if}
