<script lang="ts">
  import { Check, Lock } from "@lucide/svelte";
  import type { LearningStep } from "./types";
  import { kindLabel } from "./learning";
  let {
    steps,
    completed,
    released,
    selected,
    onselect,
  }: {
    steps: LearningStep[];
    completed: number;
    released: number;
    selected: number;
    onselect: (index: number) => void;
  } = $props();
  function state(index: number) {
    if (index < completed) return "complete";
    if (index >= released) return "waiting";
    return index === completed ? "current" : "upcoming";
  }
  const labels: Record<string, string> = {
    complete: "Complete",
    current: "Current stage",
    upcoming: "Upcoming",
    waiting: "Not released",
  };
</script>

<ol class="min-w-0">
  {#each steps as step, index (step.id)}
    {@const s = state(index)}
    <li class="relative pl-9">
      {#if index < steps.length - 1}<span
          aria-hidden="true"
          class={[
            "absolute top-8 bottom-0 left-[0.6875rem] w-px",
            index < completed ? "bg-primary/45" : "bg-base-content/15",
          ]}
        ></span>{/if}
      <span
        aria-hidden="true"
        class={[
          "absolute top-2.5 left-0 grid size-6 place-items-center rounded-full border text-xs font-medium tabular-nums",
          s === "current"
            ? "border-primary bg-primary text-primary-content"
            : s === "complete"
              ? "border-base-content/25 bg-base-300 text-base-content"
              : "border-base-content/20 bg-base-100 text-base-content/70",
        ]}
        >{#if s === "complete"}<Check size={13} />{:else if s === "waiting"}<Lock
            size={11}
          />{:else}{index + 1}{/if}</span
      >
      <button
        type="button"
        class={[
          "mb-1 flex min-h-11 w-full items-center rounded-field px-2.5 py-2 text-left hover:bg-base-200 focus-visible:outline-2 focus-visible:outline-primary",
          selected === index ? "bg-base-200" : "",
        ]}
        aria-current={s === "current" ? "step" : undefined}
        aria-pressed={selected === index}
        disabled={s === "waiting"}
        onclick={() => onselect(index)}
      >
        <span
          class={[
            "block text-sm leading-snug text-base-content/80",
            s === "current" || selected === index ? "font-medium text-base-content" : "",
            s === "waiting" ? "text-base-content/60" : "",
          ]}
          ><span class="sr-only">Stage {index + 1}: </span>{s === "waiting"
            ? `Stage ${index + 1}`
            : step.title}</span
        >
        <span class="sr-only">({kindLabel(step.kind)}, {labels[s]})</span>
      </button>
    </li>
  {/each}
</ol>
