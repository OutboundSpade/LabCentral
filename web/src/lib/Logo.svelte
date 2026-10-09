<script lang="ts">
  // A central node held inside a lab frame.
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
    stroke-width="2.6"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="stroke-primary-content"
  >
    <path d="M12 8.5H8.5v15H12" />
    <path d="M20 8.5h3.5v15H20" />
  </g>
  <circle cx="16" cy="16" r="3.4" class="fill-primary-content" />
</svg>
