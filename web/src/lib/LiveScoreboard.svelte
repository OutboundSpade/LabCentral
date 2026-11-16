<script lang="ts">
  import type { LiveScoreboard } from "./types";
  import { Clock, Trophy, RefreshCw } from "@lucide/svelte";
  let { meetingID, membershipKey = "" }: { meetingID: string; membershipKey?: string } = $props();
  let board = $state<LiveScoreboard | null>(null);
  let error = $state("");
  let loading = $state(true);
  let now = $state(Date.now());
  const own = $derived(board?.teams.find(t => t.teamID === board?.ownTeamID));
  $effect(() => {
    const scope = `${meetingID}:${membershipKey}`;
    const controller = new AbortController();
    board = null; error = ""; loading = true;
    let pending = false;
    async function load() {
      if (pending || controller.signal.aborted || !scope) return;
      pending = true;
      try {
        const response = await fetch("/api/live-scoreboard", { cache: "no-store", signal: controller.signal });
        if (!response.ok) {
          if ([401,403].includes(response.status)) board = null;
          throw new Error("Live scoring updates are unavailable. Previous results may be out of date.");
        }
        const next: LiveScoreboard = await response.json();
        if (!controller.signal.aborted && next.meetingID === meetingID) { board = next; error = ""; }
      } catch (e) { if (!controller.signal.aborted) error = (e as Error).message; }
      finally { if (!controller.signal.aborted) loading = false; pending = false; }
    }
    void load();
    const timer = setInterval(() => { now = Date.now(); void load(); }, 15000);
    return () => { controller.abort(); clearInterval(timer); };
  });
  function age(value: string) { const seconds = Math.max(0, Math.floor((now-Date.parse(value))/1000)); return seconds < 60 ? `${seconds}s ago` : `${Math.floor(seconds/60)}m ago`; }
</script>

<section class="rounded-box border border-base-content/10 bg-base-100 p-4 sm:p-6 min-w-0" aria-labelledby="live-scoring-title">
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div class="flex items-center gap-2"><Trophy size={19} aria-hidden="true"/><h2 id="live-scoring-title" class="font-semibold">Live Red vs. Blue scoreboard</h2></div>
    <span class="flex items-center gap-1 text-xs text-base-content/60"><RefreshCw size={13} aria-hidden="true"/>Updates every 15 seconds</span>
  </div>
  <p class="mt-2 text-sm text-base-content/60">Results from the scorer running inside each team's range.</p>
  {#if error}<p class="mt-3 text-sm text-warning" role="status">{error}</p>{/if}
  {#if loading}<p class="mt-4 text-sm text-base-content/60" role="status">Connecting to the lab scorer...</p>
  {:else if board}
    <div class="mt-4 space-y-2" aria-label="Team standings">
      {#each board.teams as team}
        <div class="rounded-box border border-base-content/10 p-3 flex flex-wrap items-center justify-between gap-3">
          <div class="min-w-0"><p class="font-medium break-words">{team.name}{#if team.teamID === board.ownTeamID}<span class="ml-2 text-xs text-base-content/60">Your team</span>{/if}</p>
            <p class="mt-1 text-xs text-base-content/60">{error && team.updated ? "Updates unavailable" : team.status === "live" ? "Live" : team.status === "stale" ? "Stale results" : team.status === "waiting" ? "Waiting for range" : "Scorer unavailable"}{#if team.updated}<span class="ml-2">Round {team.round} · {age(team.updated)}</span>{/if}</p></div>
          <p class="tabular-nums font-semibold">{team.updated ? `${team.earned} / ${team.possible}` : "No score yet"}</p>
        </div>
      {/each}
    </div>
    {#each board.teams.filter(t => t.services?.length && (t.teamID === board?.ownTeamID || !board?.ownTeamID)) as team}
      <h3 class="mt-6 text-sm font-semibold">{team.teamID === own?.teamID ? "Your services" : `${team.name} services`}</h3>
      <ul class="mt-2 divide-y divide-base-content/10">
        {#each team.services ?? [] as service}<li class="py-3 flex flex-wrap justify-between gap-2 text-sm">
          <span class="min-w-0 break-words">{service.name}</span><span class="flex items-center gap-3"><span class:text-success={!error && team.status === "live" && service.state === "up"} class:text-error={!error && team.status === "live" && service.state === "down"}>{team.status !== "live" || error ? "Last: " : ""}{service.state === "up" ? "Up" : service.state === "down" ? "Down" : "Unknown"}</span><span class="tabular-nums text-base-content/60">{service.earned} / {service.possible}</span></span>
        </li>{/each}
      </ul>
    {/each}
    {#if own?.updated}<p class="mt-4 flex items-center gap-1 text-xs text-base-content/60"><Clock size={13} aria-hidden="true"/>Last completed round: {new Date(own.updated).toLocaleString()}</p>{/if}
  {/if}
</section>
