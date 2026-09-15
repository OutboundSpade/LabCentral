<script lang="ts">
  import { onMount } from "svelte";
  import type ApexCharts from "apexcharts";
  import type { ApexOptions } from "apexcharts";
  let {
    title,
    description,
    series,
    percent = false,
    horizontal = false,
  }: {
    title: string;
    description: string;
    series: { name: string; data: { x: string; y: number | null }[] }[];
    percent?: boolean;
    horizontal?: boolean;
  } = $props();
  const escapeLabel = (text: string) =>
    text.replace(
      /[&<>"']/g,
      (char) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[char]!,
    );
  const chartSeries = () =>
    $state
      .snapshot(series)
      .map((group) => ({
        ...group,
        name: escapeLabel(group.name),
        data: group.data.map((point) => ({
          ...point,
          x: escapeLabel(point.x),
        })),
      }));
  let element: HTMLDivElement;
  let chart: ApexCharts | undefined;
  let ready = $state(false),
    error = $state("");
  let pending: Promise<unknown> = Promise.resolve();
  onMount(() => {
    let disposed = false;
    void (async () => {
      try {
        const { default: Apex } = await import("./charts");
        if (disposed) return;
        const options: ApexOptions = {
          chart: {
            type: "bar",
            height: 260,
            background: "transparent",
            foreColor: "var(--color-base-content)",
            toolbar: { show: false },
            animations: { enabled: false },
          },
          colors: ["var(--color-base-content)"],
          series: chartSeries(),
          plotOptions: {
            bar: { horizontal, borderRadius: 3, columnWidth: "48%" },
          },
          dataLabels: { enabled: false },
          grid: { borderColor: "var(--color-base-300)" },
          xaxis: {
            type: "category",
            labels: { show: !percent },
            ...(horizontal && percent ? { min: 0, max: 100 } : {}),
          },
          yaxis: {
            min: 0,
            ...(!horizontal && percent ? { max: 100 } : {}),
            decimalsInFloat: 0,
          },
          tooltip: {
            theme: "dark",
            y: {
              formatter: (value) =>
                value == null
                  ? "Unavailable"
                  : `${value.toFixed(percent ? 1 : 0)}${percent ? "%" : ""}`,
            },
          },
          noData: { text: "No measurements yet" },
        };
        chart = new Apex(element, options);
        pending = chart.render();
        await pending;
        if (!disposed) ready = true;
      } catch {
        if (!disposed) error = "Chart unavailable. Values are listed below.";
      }
    })();
    return () => {
      disposed = true;
      ready = false;
      void pending.catch(() => {}).then(() => chart?.destroy());
    };
  });
  $effect(() => {
    const next = chartSeries();
    if (ready && chart) {
      pending = pending
        .then(() => chart?.updateSeries(next, false))
        .catch(() => {
          error = "Chart unavailable. Values are listed below.";
        });
    }
  });
</script>

<figure class="min-w-0 rounded-box border border-base-300 p-5">
  <figcaption>
    <h2 class="font-semibold">{title}</h2>
    <p class="mt-1 text-sm text-base-content/70">{description}</p>
  </figcaption>
  <div class="min-h-64 w-full" bind:this={element} aria-label={title}></div>
  {#if error}<p role="status" class="text-sm text-warning">{error}</p>{/if}
  <details class="text-sm">
    <summary class="cursor-pointer">View {title.toLowerCase()} data</summary>
    <div class="mt-3 max-w-full overflow-x-auto">
      <table class="table table-sm">
        <thead><tr><th>Measure</th><th>Value</th></tr></thead><tbody
          >{#each series as group}{#each group.data as point}<tr
                ><td>{point.x}</td><td
                  >{point.y === null
                    ? "Unavailable"
                    : `${point.y.toFixed(percent ? 1 : 0)}${percent ? "%" : ""}`}</td
                ></tr
              >{/each}{/each}</tbody
        >
      </table>
    </div>
  </details>
</figure>
