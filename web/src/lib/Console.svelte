<script lang="ts">
  import { onMount } from "svelte";
  import {
    ArrowLeft,
    Terminal,
    Maximize,
    Minimize,
    Keyboard,
  } from "@lucide/svelte";
  import type RFB from "@novnc/novnc";
  let { accessSessionId }: { accessSessionId: string } = $props();
  let consoleStatus = $state("Connecting to your lab…");
  let consoleTarget = $state<HTMLDivElement>();
  let consoleClient: RFB | null = null;
  let consoleSocket: WebSocket | null = null;
  let consoleRoot = $state<HTMLElement>();
  let connected = $state(false);
  let fullscreen = $state(false);
  let resizeMode = $state("fit");
  let viewOnly = $state(false);
  let specialKey = $state("escape");
  let controlError = $state("");
  function resize() {
    if (!consoleClient) return;
    consoleClient.resizeSession = resizeMode === "remote";
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
    if (!connected || viewOnly || !consoleClient) return;
    if (specialKey === "ctrl-alt-delete") consoleClient.sendCtrlAltDel();
    else if (specialKey === "ctrl-c") {
      consoleClient.sendKey(0xffe3, "ControlLeft", true);
      consoleClient.sendKey(0x0063, "KeyC");
      consoleClient.sendKey(0xffe3, "ControlLeft", false);
    } else consoleClient.sendKey(specialKey === "tab" ? 0xff09 : 0xff1b);
    consoleClient.focus();
  }
  let disposed = false;
  onMount(() => {
    void connectConsole();
    const changed = () => {
      fullscreen = !!document.fullscreenElement;
    };
    document.addEventListener("fullscreenchange", changed);
    return () => {
      disposed = true;
      document.removeEventListener("fullscreenchange", changed);
      consoleClient?.disconnect();
      consoleSocket?.close();
    };
  });
  async function connectConsole() {
    const tokenKey = `labcentral.access-token.v1.${accessSessionId}`;
    const token = sessionStorage.getItem(tokenKey);
    sessionStorage.removeItem(tokenKey);
    if (!token || !consoleTarget) {
      consoleStatus = "Open the console again from your lab.";
      return;
    }
    let ConsoleClient: typeof RFB;
    try {
      ({ default: ConsoleClient } = await import("@novnc/novnc"));
    } catch {
      consoleStatus =
        "The console could not load. Open it again from your lab.";
      return;
    }
    if (disposed) return;
    const target = consoleTarget;
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(
      `${scheme}//${window.location.host}/access-proxy/v1/access/${encodeURIComponent(accessSessionId)}/websocket`,
      ["binary"],
    );
    consoleSocket = socket;
    socket.binaryType = "arraybuffer";
    socket.addEventListener(
      "open",
      () => {
        socket.send(JSON.stringify({ accessSessionId, oneTimeToken: token }));
      },
      { once: true },
    );
    socket.addEventListener(
      "message",
      (event) => {
        if (event.data !== '{"authorized":true}') {
          consoleStatus =
            "Unable to connect. Open the console again from your lab.";
          socket.close();
          return;
        }
        try {
          consoleClient = new ConsoleClient(target, socket, { shared: false });
          resize();
          consoleClient.viewOnly = viewOnly;
          consoleClient.addEventListener("connect", () => {
            consoleStatus = "Connected";
            connected = true;
            consoleClient?.focus();
          });
          consoleClient.addEventListener("disconnect", () => {
            connected = false;
            consoleStatus =
              "Disconnected. Open the console again from your lab.";
          });
          consoleClient.addEventListener("securityfailure", () => {
            consoleStatus = "Unable to connect to this console.";
          });
        } catch {
          consoleStatus = "The console client could not start.";
          socket.close();
        }
      },
      { once: true },
    );
    socket.addEventListener(
      "error",
      () => {
        if (!consoleClient) consoleStatus = "The console is unavailable.";
      },
      { once: true },
    );
  }
</script>

<svelte:head><title>Lab console · LabCentral</title></svelte:head>
<main
  bind:this={consoleRoot}
  class="flex h-dvh flex-col gap-3 bg-base-100 p-3 text-base-content"
>
  <header class="flex flex-wrap items-center gap-4">
    <a href="/" class="btn btn-sm btn-ghost"><ArrowLeft size={16} /> My labs</a
    ><Terminal size={18} />
    <h1 class="text-sm font-semibold">Lab console</h1>
    <p class="text-sm text-base-content/70" aria-live="polite">
      {consoleStatus}
    </p>
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
        disabled={!connected || viewOnly}
      >
        <option value="escape">Escape</option><option value="tab">Tab</option
        ><option value="ctrl-c">Ctrl-C</option><option value="ctrl-alt-delete"
          >Ctrl-Alt-Delete</option
        >
      </select>
      <button
        class="btn btn-sm"
        disabled={!connected || viewOnly}
        onclick={sendSpecialKey}><Keyboard size={16} />Send key</button
      >
    </div>
    <label class="flex items-center gap-2 text-sm"
      ><input
        type="checkbox"
        class="checkbox checkbox-sm"
        aria-label="View only"
        bind:checked={viewOnly}
        onchange={() => {
          if (consoleClient) consoleClient.viewOnly = viewOnly;
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
  {#if controlError}<p role="alert" class="text-sm text-error">
      {controlError}
    </p>{/if}
  <div
    bind:this={consoleTarget}
    class="min-h-0 flex-1 overflow-hidden rounded-lg border border-base-300 bg-neutral"
  ></div>
</main>
