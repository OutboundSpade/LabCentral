<script lang="ts">
  // A stage rail with its current checkpoint joined to an open workspace.
  let { size = 28 }: { size?: number } = $props();
</script>

<svg
  viewBox="0 0 32 32"
  width={size}
  height={size}
  aria-hidden="true"
  class="shrink-0"
>
  <rect width="32" height="32" rx="8" class="fill-primary" />
  <g
    fill="none"
    stroke-width="2.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="stroke-primary-content"
  >
    <path d="M10 7.5v17M10 16h5" />
    <rect x="15" y="10.5" width="9.5" height="11" rx="2" />
  </g>
  <circle cx="10" cy="16" r="3.25" class="fill-primary-content" />
</svg>
