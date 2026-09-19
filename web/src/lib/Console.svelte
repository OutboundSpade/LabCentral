<script lang="ts">
  import { onMount } from "svelte";
  import {
    ArrowLeft,
    Terminal,
    Maximize,
    Minimize,
    Keyboard,
  } from "@lucide/svelte";
  import { replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import {
    api,
    ApiError,
    mutationHeaders,
    type Instance,
    type AccessSession,
    type AccessLaunch,
  } from "./api";
  import type RFB from "@novnc/novnc";
  import type { components } from "./api.generated";
  type Presence=components["schemas"]["ConsolePresence"];
  let { accessSessionId }: { accessSessionId: string } = $props();
  let consoleStatus = $state("Connecting to your lab…");
  let consoleTarget = $state<HTMLDivElement>();
  let consoleClient: RFB | null = null;
  let consoleSocket: WebSocket | null = null;
  let consoleRoot = $state<HTMLElement>();
  let connected = $state(false);
  let fullscreen = $state(false);
  let resizeMode = $state("fit");
  let currentID = $state("");
  let viewOnly = $state(false);
  let sharedConsole = $state(false);
  let presence = $state<Presence>();
  let controlBusy = $state(false);
  let presenceTimer: ReturnType<typeof setTimeout> | undefined;
  let hasControl = $derived(!!presence && presence.driverSessionId===currentID && presence.requestedSessionId===currentID);
  let effectiveViewOnly = $derived(viewOnly || (sharedConsole && !hasControl));
  $effect(() => { const only=effectiveViewOnly; const mode=resizeMode; if(consoleClient) {consoleClient.viewOnly=only;consoleClient.resizeSession=mode==="remote"&&!only;consoleClient.scaleViewport=mode!=="actual";} });
  async function refreshPresence(id:string,mine:number) {
    if(disposed || id!==currentID || mine!==generation || !connected || !sharedConsole)return;
    try {
      const result=await api<Presence>(`/access-sessions/${encodeURIComponent(id)}/console`,{fresh:true});
      if(!disposed && id===currentID && mine===generation)presence=result;
    } catch {if(id===currentID && mine===generation)presence=undefined;}
    if(!disposed && id===currentID && mine===generation && connected)presenceTimer=setTimeout(()=>void refreshPresence(id,mine),1000);
  }
  async function changeControl(take:boolean) {
    if(controlBusy || !connected)return;
    const id=currentID,mine=generation;
    controlBusy=true;controlError="";
    try {
      const result=await api<Presence>(`/access-sessions/${encodeURIComponent(id)}/console/${take?"take-control":"release-control"}`,{method:"POST",headers:mutationHeaders()});
      if(id===currentID && mine===generation)presence=result;
    } catch(e) {if(id===currentID && mine===generation)controlError=e instanceof Error?e.message:"Control could not be changed.";}
    finally {controlBusy=false;}
  }
  let specialKey = $state("escape");
  let controlError = $state("");
  function resize() {
    if (!consoleClient) return;
    consoleClient.resizeSession = resizeMode === "remote" && !effectiveViewOnly;
    consoleClient.scaleViewport = resizeMode !== "actual";
  }
  async function toggleFullscreen() {
    controlError = "";
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else await consoleRoot?.requestFullscreen();
    } catch {
      controlError = "Full screen is unavailable in this browser.";
    }
  }
  function sendSpecialKey() {
    if (!connected || effectiveViewOnly || !consoleClient) return;
    if (specialKey === "ctrl-alt-delete") consoleClient.sendCtrlAltDel();
    else if (specialKey === "ctrl-c") {
      consoleClient.sendKey(0xffe3, "ControlLeft", true);
      consoleClient.sendKey(0x0063, "KeyC");
      consoleClient.sendKey(0xffe3, "ControlLeft", false);
    } else consoleClient.sendKey(specialKey === "tab" ? 0xff09 : 0xff1b);
    consoleClient.focus();
  }
  let lab = $state<Instance>();
  let resourceID = $state("");
  async function loadMachines(id: string) {
    try {
      const session = await api<AccessSession>(
        `/access-sessions/${encodeURIComponent(id)}`,
      );
      if(disposed || id!==currentID)return;
      sharedConsole=!!session.sharedConsole;
      if(connected && sharedConsole){clearTimeout(presenceTimer);void refreshPresence(id,generation);}
      const instance = await api<Instance>(
        `/lab-instances/${encodeURIComponent(session.instanceId)}`,
        { signal: request?.signal ?? AbortSignal.timeout(10000) },
      );
      if (disposed || id !== currentID) return;
      lab = instance;
      resourceID = session.resourceId;
    } catch {
      /* Console transport remains usable if metadata cannot load. */
    }
  }
  let disposed = false;
  let attempt = 0,
    generation = 0;
  let reconnecting = $state(false),
    denied = $state(false);
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let handshakeTimer: ReturnType<typeof setTimeout> | undefined;
  let request: AbortController | undefined;
  function closeTransport() {
    clearTimeout(presenceTimer);
    presence=undefined;
    clearTimeout(handshakeTimer);
    consoleClient?.disconnect();
    consoleClient = null;
    consoleSocket?.close();
    consoleSocket = null;
    connected = false;
  }
  onMount(() => {
    currentID = accessSessionId;
    void loadMachines(currentID);
    void connectConsole();
    const changed = () => {
      fullscreen = !!document.fullscreenElement;
    };
    document.addEventListener("fullscreenchange", changed);
    return () => {
      disposed = true;
      generation++;
      clearTimeout(retryTimer);
      request?.abort();
      closeTransport();
      document.removeEventListener("fullscreenchange", changed);
    };
  });
  function retry(message: string, mine: number) {
    if (disposed || mine !== generation) return;
    generation++;
    closeTransport();
    reconnecting = false;
    if (attempt >= 5 || denied) {
      consoleStatus = message + " Use Reconnect to try again.";
      return;
    }
    const delay = Math.min(16000, 1000 * 2 ** attempt++);
    consoleStatus = `Connection lost. Reconnecting in ${delay / 1000}s…`;
    reconnecting = true;
    retryTimer = setTimeout(() => void connectConsole(), delay);
  }
  async function connectConsole(selectedResource?: string) {
    clearTimeout(retryTimer);
    const mine = ++generation;
    request?.abort();
    request = new AbortController();
    const signal = request.signal;
    closeTransport();
    reconnecting = true;
    denied = false;
    consoleStatus = "Connecting to your lab…";
    const tokenKey = `labcentral.access-token.v1.${currentID}`;
    let token = selectedResource ? null : sessionStorage.getItem(tokenKey);
    sessionStorage.removeItem(tokenKey);
    try {
      if (!token) {
        const previous = await api<AccessSession>(
          `/access-sessions/${encodeURIComponent(currentID)}`,
          { signal },
        );
        if (disposed || mine !== generation) return;
        if (
          previous.state === "revoked" ||
          previous.revokedAt ||
          previous.closureRequired
        ) {
          denied = true;
          reconnecting = false;
          consoleStatus = "Console access was revoked. Return to your lab.";
          return;
        }
        const launch = await api<AccessLaunch>("/access-sessions", {
          method: "POST",
          signal,
          headers: mutationHeaders(),
          body: JSON.stringify({
            instanceId: previous.instanceId,
            resourceId: selectedResource ?? previous.resourceId,
            method: previous.method,
            durationSeconds: 1800,
          }),
        });
        if (disposed || mine !== generation) return;
        const expected = `/access/${encodeURIComponent(launch.session.id)}`;
        const destination = new URL(launch.launchUrl, location.origin);
        if (
          !launch.oneTimeToken ||
          destination.origin !== location.origin ||
          destination.pathname !== expected ||
          destination.search ||
          destination.hash ||
          destination.username ||
          destination.password
        )
          throw new Error("Invalid console launch response.");
        currentID = launch.session.id;
        resourceID = launch.session.resourceId;
        void loadMachines(currentID);
        token = launch.oneTimeToken;
        replaceState(expected, page.state);
        // Retire the replaced access grant. Failure is harmless: its bounded
        // expiry and revocation checks still apply; do not block the new socket.
        void api(
          `/access-sessions/${encodeURIComponent(previous.id)}/actions/revoke`,
          {
            method: "POST",
            signal,
            headers: {
              ...mutationHeaders(),
              "If-Match": `"${previous.resourceVersion}"`,
            },
            body: JSON.stringify({ reason: "console-reconnected" }),
          },
        ).catch(() => {});
      }
      const presentation = await api<{streamPath?:string}>(`/access-sessions/${encodeURIComponent(currentID)}/presentation`, {signal});
      if (!presentation.streamPath?.startsWith("/" ) || presentation.streamPath.startsWith("//")) throw new Error("Console stream unavailable");
      const { default: ConsoleClient } = await import("@novnc/novnc");
      if (disposed || mine !== generation || !consoleTarget) return;
      const scheme = location.protocol === "https:" ? "wss:" : "ws:";
      const socket = new WebSocket(
        `${scheme}//${location.host}${presentation.streamPath}`,
        ["binary"],
      );
      consoleSocket = socket;
      socket.binaryType = "arraybuffer";
      handshakeTimer = setTimeout(
        () => retry("The console did not respond.", mine),
        15000,
      );
      socket.addEventListener(
        "open",
        () => {
          if (mine === generation)
            socket.send(
              JSON.stringify({
                accessSessionId: currentID,
                oneTimeToken: token,
              }),
            );
        },
        { once: true },
      );
      socket.addEventListener(
        "message",
        (event) => {
          if (disposed || mine !== generation) return;
          if (event.data !== '{"authorized":true}') {
            retry("Console authorization failed.", mine);
            return;
          }
          try {
            const client = new ConsoleClient(consoleTarget!, socket, {
              shared: true,
            });
            consoleClient = client;
            resize();
            client.viewOnly = effectiveViewOnly;
            client.addEventListener("connect", () => {
              if (disposed || mine !== generation) return;
              clearTimeout(handshakeTimer);
              consoleStatus = "Connected";
              connected = true;
              if(sharedConsole)void refreshPresence(currentID,mine);
              reconnecting = false;
              client.focus();
            });
            client.addEventListener("disconnect", () =>
              retry("The console disconnected.", mine),
            );
            client.addEventListener("securityfailure", () => {
              if (mine !== generation) return;
              denied = true;
              retry("Console security negotiation failed.", mine);
            });
          } catch {
            retry("The console client could not start.", mine);
          }
        },
        { once: true },
      );
      socket.addEventListener(
        "close",
        () => retry("The console is unavailable.", mine),
        { once: true },
      );
      socket.addEventListener(
        "error",
        () => retry("The console is unavailable.", mine),
        { once: true },
      );
    } catch (e) {
      if (disposed || mine !== generation) return;
      if (
        e instanceof ApiError &&
        [400, 401, 403, 404, 409, 422].includes(e.status)
      ) {
        denied = true;
        reconnecting = false;
        consoleStatus =
          e.status === 401
            ? "Sign in again to open your console."
            : "Console access is unavailable. Return to your lab.";
      } else
        retry(
          e instanceof Error ? e.message : "The console is unavailable.",
          mine,
        );
    }
  }
</script>

<svelte:head><title>Lab console · LabCentral</title></svelte:head>
<main
  bind:this={consoleRoot}
  class="flex h-dvh flex-col gap-3 bg-base-100 p-3 text-base-content"
>
  <header class="flex flex-wrap items-center gap-4">
    <a
      href={lab ? `/lab/?id=${encodeURIComponent(lab.id)}` : "/"}
      class="btn btn-sm btn-ghost"
      ><ArrowLeft size={16} /> {lab ? "Lab details" : "My labs"}</a
    ><Terminal size={18} />
    <h1 class="text-sm font-semibold">Lab console</h1>
    {#if lab?.resources?.length}
      <select
        class="select select-sm w-auto"
        aria-label="Machine"
        value={resourceID}
        disabled={reconnecting}
        onchange={(event) => {
          attempt = 0;
          void connectConsole(event.currentTarget.value);
        }}
      >
        {#each lab.resources.filter((r) => (r.kind === "VM" || r.virtualMachine) && r.phase === "ready") as resource}
          <option value={resource.id}
            >{resource.id.replace(/^[^.]+\./, "")}</option
          >
        {/each}
      </select>
    {/if}
    <p class="text-sm text-base-content/70" aria-live="polite">
      {consoleStatus}
    </p>
    {#if !connected}<button
        class="btn btn-sm ml-auto"
        disabled={reconnecting || denied}
        onclick={() => {
          attempt = 0;
          void connectConsole();
        }}>Reconnect</button
      >{/if}
  </header>
  <div
    class="flex flex-wrap items-center gap-3 border-b border-base-300 pb-3"
    aria-label="Console controls"
  >
    <label class="flex items-center gap-2 text-sm"
      >Resize
      <select
        class="select select-sm w-40"
        aria-label="Resize"
        bind:value={resizeMode}
        onchange={resize}
      >
        <option value="fit">Fit to window</option><option value="actual"
          >Actual size</option
        ><option value="remote">Resize remote</option>
      </select>
    </label>
    <div class="flex items-center gap-2">
      <select
        aria-label="Special key"
        class="select select-sm w-40"
        bind:value={specialKey}
        disabled={!connected || effectiveViewOnly}
      >
        <option value="escape">Escape</option><option value="tab">Tab</option
        ><option value="ctrl-c">Ctrl-C</option><option value="ctrl-alt-delete"
          >Ctrl-Alt-Delete</option
        >
      </select>
      <button
        class="btn btn-sm"
        disabled={!connected || effectiveViewOnly}
        onclick={sendSpecialKey}><Keyboard size={16} />Send key</button
      >
    </div>
    <label class="flex items-center gap-2 text-sm"
      ><input
        type="checkbox"
        class="checkbox checkbox-sm"
        aria-label="View only"
        disabled={sharedConsole && !hasControl}
        bind:checked={viewOnly}
        onchange={() => {
          if (consoleClient) consoleClient.viewOnly = effectiveViewOnly;
        }}
      />View only</label
    >
    <button
      class="btn btn-sm ml-auto"
      onclick={toggleFullscreen}
      aria-pressed={fullscreen}
    >
      {#if fullscreen}<Minimize size={16} />Exit full screen{:else}<Maximize
          size={16}
        />Full screen{/if}
    </button>
  </div>
  {#if sharedConsole}
    <div class="flex flex-wrap items-center gap-3 text-sm" aria-label="Shared console">
      <span aria-live="polite">{hasControl?"You have control":presence?.requestedSessionId===currentID?"Waiting for control…":"Watching"}</span>
      <button class="btn btn-sm" disabled={!connected || controlBusy} onclick={()=>void changeControl(!hasControl)}>{hasControl?"Release control":"Take control"}</button>
      {#if presence?.participants?.length}
        <span>Connected: {presence.participants.map(p=>p.displayName+(p.sessionId===presence?.driverSessionId?" (driver)":"")).join(", ")}</span>
      {/if}
    </div>
  {/if}
  {#if controlError}<p role="alert" class="text-sm text-error">
      {controlError}
    </p>{/if}
  <div
    bind:this={consoleTarget}
    class="min-h-0 flex-1 overflow-hidden rounded-lg border border-base-300 bg-neutral"
  ></div>
</main>
