<script lang="ts">
  import { Monitor, SquareTerminal, Unplug, Server } from "@lucide/svelte";
  import type { Machine, Meeting, Team } from "./types";
  import { api, APIError, refresh } from "./api";
  import { launchDesktop } from "./launch";
  import { terminalURL } from "./terminal";
  import Status from "./Status.svelte";

  let { machine, team, meeting, instance }: { machine: Machine; team: Team; meeting: Meeting; instance: string } = $props();
  let busy = $state(false);
  let error = $state("");
  let conflict = $state("");
  let confirm = $state(false);
  const ready = $derived(team.status === "ready" && machine.status === "ready");
  const base = $derived(`/api/machines/${encodeURIComponent(machine.name)}/desktop`);
  const holder = $derived(conflict || (machine.desktopState?.code === "held" ? machine.desktopState.holderName || "A teammate" : ""));
  const unknown = $derived(machine.desktopState?.code === "desktop_outcome_unknown");
  const terminal = $derived(terminalURL({ team: team.id, machine: machine.name, meeting: meeting.id, template: meeting.template, instance }, false));

  async function open(takeover = false) {
    busy = true;
    error = "";
    confirm = false;
    try {
      await launchDesktop(base + (takeover ? "/takeover" : ""), `Starting ${machine.name}…`);
      conflict = "";
    } catch (e) {
      if (e instanceof APIError && e.status === 409 && e.code.startsWith("desktop_")) {
        conflict = String(e.body.holderName || e.body.takenOverBy || "A teammate");
      } else error = (e as Error).message;
    } finally { busy = false; await refresh(); }
  }
  async function close() {
    busy = true;
    error = "";
    try { await api(base + "/close", {}); }
    catch (e) { error = (e as Error).message; }
    finally { busy = false; await refresh(); }
  }
</script>

<section class="rounded-box border border-base-content/10 bg-base-100 p-4 min-w-0" aria-label={machine.name}>
  <div class="flex items-start gap-3">
    <Server size={18} class="mt-0.5 shrink-0 text-base-content/55" aria-hidden="true" />
    <div class="min-w-0 flex-1">
      <h3 class="break-words font-medium">{machine.name}</h3>
      <p class="mt-1 text-xs text-base-content/60">{machine.appliance === "vyos" ? "VyOS router" : machine.appliance === "panos" ? "Palo Alto firewall" : machine.os === "windows" ? "Windows" : "Linux"}</p>
    </div>
    <Status status={machine.status} />
  </div>
  <div class="mt-4 flex flex-wrap gap-2">
    {#if machine.desktop}
      <button class="btn btn-sm btn-primary min-h-11" disabled={!ready || busy || unknown} onclick={() => holder ? (confirm = true) : open()} aria-label={`Open ${machine.name} desktop`}>
        {#if busy}<span class="loading loading-spinner loading-xs"></span>{:else}<Monitor size={15} aria-hidden="true" />{/if}
        {holder ? "Take over desktop" : "Desktop"}
      </button>
    {/if}
    {#if machine.ssh}
      {#if ready}<a class="btn btn-sm min-h-11" href={terminal} target="_blank" rel="noopener noreferrer" aria-label={`Open ${machine.name} terminal`}><SquareTerminal size={15} aria-hidden="true" />Terminal</a>
      {:else}<button class="btn btn-sm min-h-11" disabled><SquareTerminal size={15} aria-hidden="true" />Terminal</button>{/if}
    {/if}
    {#if machine.desktopActive}<button class="btn btn-sm btn-ghost min-h-11" disabled={busy} onclick={close} aria-label={`Disconnect ${machine.name} desktop`}><Unplug size={15} aria-hidden="true" />Disconnect</button>{/if}
  </div>
  {#if !machine.desktop && !machine.ssh}<p class="mt-3 text-xs text-base-content/60">No interactive access is configured for this machine.</p>{/if}
  {#if machine.managementURL}<p class="mt-3 text-xs leading-relaxed text-base-content/70">Open <code class="break-all">{machine.managementURL}</code> in a lab workstation's browser for HTTPS management. Use Terminal to configure the device and its web login.</p>{/if}
  {#if holder}<p class="mt-3 text-sm text-base-content/70" role="status">{holder} is using this desktop.</p>{/if}
  {#if unknown}<p class="mt-3 text-sm text-base-content/70" role="status">The previous desktop is closing. Access will return after cleanup is confirmed.</p>{/if}
  {#if confirm}<div class="mt-3 rounded-box border border-warning/30 bg-warning/8 p-3">
    <p class="text-sm">Take over {machine.name} from {holder}? Saved files stay; unsaved work in open programs can be lost.</p>
    <div class="mt-3 flex flex-wrap gap-2"><button class="btn btn-sm btn-warning min-h-11" disabled={busy} onclick={() => open(true)}>Confirm take over</button><button class="btn btn-sm btn-ghost min-h-11" onclick={() => (confirm = false)}>Cancel</button></div>
  </div>{/if}
  {#if error}<p class="mt-3 text-sm text-error" role="alert">{error}</p>{/if}
</section>
