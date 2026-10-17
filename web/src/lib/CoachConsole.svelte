<script lang="ts">
  import { Monitor, RotateCw, SquareArrowOutUpRight, SquareTerminal } from "@lucide/svelte";
  import { api, APIError, session } from "$lib/api";
  import Notice from "$lib/Notice.svelte";
  import { launchDesktop } from "$lib/launch";
  import { parseInventory, terminalURL, type TerminalInventory } from "$lib/terminal";
  import type { AdminView } from "$lib/types";

  let { data }: { data: AdminView } = $props();
  let teamID = $state("");
  let machine = $state("");
  let inventory = $state<TerminalInventory | null>(null);
  let error = $state("");
  let loading = $state(false);
  let opening = $state(false);
  // Set when the team's desktop is in use: the coach may take it over.
  let held = $state("");
  let sequence = 0;
  let scope = "";
  const ready = $derived(data.teams.filter((t) => t.status === "ready"));
  // An inventory only counts for the team, meeting and revision on screen.
  const pinned = $derived(
    inventory &&
      inventory.teamID === teamID &&
      inventory.meetingID === data.meeting?.id &&
      inventory.template === data.meeting?.template
      ? inventory
      : null,
  );
  const href = $derived(
    pinned && pinned.machines.some((m) => m.name === machine && m.status === "ready")
      ? terminalURL({
          team: pinned.teamID,
          machine,
          meeting: pinned.meetingID,
          template: pinned.template,
          instance: pinned.instanceID,
        })
      : null,
  );

  // Session, CSRF, meeting or revision changes discard the selection and any late inventory.
  $effect(() => {
    const next = $session?.user.admin
      ? JSON.stringify([$session.user.id, $session.csrf, data.meeting?.id ?? "", data.meeting?.template ?? ""])
      : "";
    if (next !== scope || (teamID && !ready.some((t) => t.id === teamID))) {
      scope = next;
      sequence++;
      teamID = "";
      machine = "";
      held = "";
      inventory = null;
      error = "";
      loading = false;
    }
  });

  async function openDesktop(takeover: boolean) {
    const team = teamID;
    if (!team) return;
    opening = true;
    error = "";
    try {
      await launchDesktop(
        `/api/admin/teams/${encodeURIComponent(team)}/desktop${takeover ? "/takeover" : ""}`,
        "Starting the team desktop. Windows can take a few minutes…",
      );
      held = "";
    } catch (e) {
      if (e instanceof APIError && e.status === 409 && e.code.startsWith("desktop_"))
        held = String(e.body.holderName ?? e.body.takenOverBy ?? "A student");
      else error = (e as Error).message;
    } finally {
      opening = false;
    }
  }

  async function load() {
    const version = ++sequence;
    held = "";
    inventory = null;
    machine = "";
    error = "";
    const requested = teamID;
    if (!requested) return void (loading = false);
    loading = true;
    try {
      const result = parseInventory(
        await api<unknown>(`/api/admin/teams/${encodeURIComponent(requested)}/machines`),
      );
      if (version !== sequence) return;
      if (!result) {
        error = "The machine list was incomplete. Refresh machines to try again.";
        return;
      }
      if (result.teamID !== requested) {
        error = "The machine list belonged to a different team, so it was not used. Refresh machines to try again.";
        return;
      }
      inventory = result;
      if (result.meetingID !== data.meeting?.id || result.template !== data.meeting?.template) {
        error = "The meeting or its revision changed while machines were loading. Choose the team again.";
        return;
      }
      machine = result.machines.find((m) => m.status === "ready")?.name ?? "";
    } catch (e) {
      if (version === sequence)
        error = e instanceof APIError ? e.message : "Machines could not be loaded. Check your connection, then refresh machines.";
    } finally {
      if (version === sequence) loading = false;
    }
  }
</script>

<section class="mt-6 border-t border-base-content/10 pt-5" aria-labelledby="coach-console-title">
  <h2 id="coach-console-title" class="flex items-center gap-2 text-sm font-semibold">
    <SquareTerminal size={16} aria-hidden="true" class="text-primary" />Coach machine access
  </h2>
  <p id="coach-ssh-hint" class="mt-1 text-xs leading-relaxed text-base-content/70">
    Opens a terminal or the team's desktop in a new tab.
  </p>
  <Notice message={error} />
  <div class="mt-4 flex flex-wrap items-end gap-3">
    <fieldset class="fieldset min-w-44 flex-1">
      <label class="label" for="coach-team">Team</label>
      <select id="coach-team" class="select select-sm min-h-11 w-full" bind:value={teamID} onchange={load}>
        <option value="">Choose a ready team</option>
        {#each ready as team (team.id)}<option value={team.id}>{team.name}</option>{/each}
      </select>
    </fieldset>
    <fieldset class="fieldset min-w-44 flex-1">
      <label class="label" for="coach-machine">Machine</label>
      <select
        id="coach-machine"
        class="select select-sm min-h-11 w-full"
        bind:value={machine}
        disabled={loading || !pinned?.machines.length}
      >
        <option value=""
          >{loading
            ? "Loading machines…"
            : pinned && !pinned.machines.length
              ? "No SSH machines in this revision"
              : "Choose a machine"}</option
        >
        {#each pinned?.machines ?? [] as m (m.name)}<option value={m.name} disabled={m.status !== "ready"}
            >{m.status === "ready" ? m.name : `${m.name} (${m.status})`}</option
          >{/each}
      </select>
    </fieldset>
    <div class="flex flex-wrap items-center gap-2">
      {#if href}<a
          {href}
          target="_blank"
          rel="noopener"
          class="btn btn-sm btn-primary min-h-11"
          aria-describedby="coach-ssh-hint"
          ><SquareTerminal size={15} aria-hidden="true" />Open SSH<SquareArrowOutUpRight
            size={14}
            aria-hidden="true"
          /></a
        >{:else}<button type="button" class="btn btn-sm btn-primary min-h-11" disabled
          ><SquareTerminal size={15} aria-hidden="true" />Open SSH</button
        >{/if}
      <button
        type="button"
        class="btn btn-sm min-h-11"
        disabled={!teamID || opening}
        aria-busy={opening}
        onclick={() => openDesktop(false)}
        >{#if opening}<span class="loading loading-spinner loading-xs"></span>{:else}<Monitor
            size={15}
            aria-hidden="true"
          />{/if}Open desktop</button
      >
      {#if teamID && !loading && (error || !pinned)}<button type="button" class="btn btn-sm min-h-11" onclick={load}
          ><RotateCw size={15} aria-hidden="true" />Refresh machines</button
        >{/if}
    </div>
  </div>
  {#if held && teamID}<div class="mt-3 rounded-box border border-warning/30 bg-warning/8 p-3" role="status">
      <p class="text-xs leading-relaxed">
        {held} is using this team's desktop. Taking over disconnects them; saved files stay.
      </p>
      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="btn btn-sm btn-warning min-h-11 lg:min-h-8" disabled={opening} onclick={() => openDesktop(true)}
          >Take over</button
        ><button type="button" class="btn btn-sm btn-ghost min-h-11 lg:min-h-8" onclick={() => (held = "")}>Cancel</button>
      </div>
    </div>{/if}
</section>
