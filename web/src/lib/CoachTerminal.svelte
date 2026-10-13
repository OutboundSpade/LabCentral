<script lang="ts">
  import "@xterm/xterm/css/xterm.css";
  import { onMount } from "svelte";
  import type { Terminal } from "@xterm/xterm";
  import type { FitAddon } from "@xterm/addon-fit";
  import {
    AArrowDown,
    AArrowUp,
    CircleCheck,
    CircleStop,
    Copy,
    RotateCw,
    Server,
    ShieldX,
    SquareTerminal,
    Timer,
    TriangleAlert,
    Unplug,
    Users,
    WifiOff,
  } from "@lucide/svelte";
  import { session } from "$lib/api";
  import {
    RETRY_DELAYS,
    TerminalConnection,
    clampSize,
    mirrorRuntimeStyles,
    parseTarget,
    terminalTheme,
    type TerminalState,
    type TerminalTarget,
  } from "$lib/terminal";

  const MONO =
    'ui-monospace, "SFMono-Regular", "Cascadia Mono", "DejaVu Sans Mono", Menlo, Consolas, "Liberation Mono", monospace';
  const ACTIVE = ["authorizing", "connecting", "connected", "retrying", "offline"];
  // Output accepted by the renderer but not yet parsed. xterm would otherwise
  // queue without limit (and throws at ~50 MB); past this the shell is closed.
  const MAX_PENDING_OUTPUT = 4 * 1024 * 1024;

  // One xterm instance in its own mount element. Clearing the transcript
  // disposes it and mounts a new one: xterm's reset() keeps queued writes,
  // which would otherwise repaint output after a revocation.
  interface Renderer {
    t: Terminal;
    fit: FitAddon;
    mount: HTMLDivElement;
    pending: number;
  }

  let target = $state<TerminalTarget | null>(null);
  let missing = $state(false);
  let failed = $state("");
  let terminalReady = $state(false);
  let view = $state<TerminalState | null>(null);
  let fontSize = $state(14);
  let copyNote = $state("");
  let now = $state(Date.now());
  let fitHeight = $state<number | null>(null);
  let host = $state<HTMLDivElement>();
  let workspace = $state<HTMLElement>();
  let term = $state.raw<Terminal | null>(null);
  let connection: TerminalConnection | null = null;
  let renderer: Renderer | null = null;
  let modules: { xterm: typeof import("@xterm/xterm"); fit: typeof import("@xterm/addon-fit") } | null = null;
  let copyTimer = 0;

  const phase = $derived(view?.phase ?? "authorizing");
  const inventory = $derived(view?.inventory ?? null);
  const active = $derived(ACTIVE.includes(phase));
  const seconds = $derived(
    view?.retryAt ? Math.max(0, Math.ceil((view.retryAt - now) / 1000)) : 0,
  );
  const renews = $derived(
    view?.expires && phase === "connected"
      ? new Date(view.expires).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
      : "",
  );
  const label = $derived(
    ({
      authorizing: "Checking access",
      connecting: "Starting shell",
      connected: "Connected",
      retrying: "Reconnecting",
      offline: "Offline",
      ended: "Shell exited",
      disconnected: "Disconnected",
      exhausted: "Could not reconnect",
      denied: "Access stopped",
    })[phase],
  );
  // Names are display metadata and may be empty; fall back to the pinned IDs.
  const teamLabel = $derived(inventory?.teamName || target?.team || "Team");
  const meetingLabel = $derived(inventory?.meetingName || target?.meeting || "Meeting");
  // While an on-screen keyboard shrinks the visible area, drop secondary
  // chrome so the shell and its key strip stay above the keyboard.
  const compact = $derived(fitHeight !== null && fitHeight < 560);
  const title = $derived(target ? `${target.machine} · ${teamLabel} · LabCentral` : "Machine console · LabCentral");

  $effect(() => {
    if (term) term.options.disableStdin = phase !== "connected";
  });
  $effect(() => {
    if (phase !== "retrying") return;
    now = Date.now();
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  function refit() {
    const r = renderer;
    if (!r) return;
    const t = r.t;
    try {
      r.fit.fit();
    } catch {
      return;
    }
    const { cols, rows } = clampSize({ cols: t.cols, rows: t.rows });
    if (cols !== t.cols || rows !== t.rows) t.resize(cols, rows);
    connection?.resize();
  }
  let frame = 0;
  function scheduleFit() {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(refit);
  }
  function setFont(size: number) {
    fontSize = Math.min(24, Math.max(10, size));
    if (renderer) renderer.t.options.fontSize = fontSize;
    scheduleFit();
  }
  // Keep the shell above an on-screen keyboard: follow the visual viewport.
  function followViewport() {
    const vv = window.visualViewport;
    if (!vv || !workspace) return;
    if (innerHeight - vv.height < 2) fitHeight = null;
    else fitHeight = Math.max(120, Math.round(vv.offsetTop + vv.height - workspace.getBoundingClientRect().top));
    scheduleFit();
  }

  function note(message: string) {
    copyNote = message;
    clearTimeout(copyTimer);
    copyTimer = window.setTimeout(() => (copyNote = ""), 6000);
  }
  async function copySelection() {
    const text = term?.getSelection() ?? "";
    if (!text) return note("Select text in the terminal first, then copy it.");
    try {
      await navigator.clipboard.writeText(text);
      note(`Copied ${text.length} characters.`);
    } catch {
      // Fallback: the browser's own copy command, which xterm fills from its selection.
      term?.focus();
      let copied = false;
      try {
        copied = document.execCommand("copy");
      } catch {
        copied = false;
      }
      note(
        copied
          ? `Copied ${text.length} characters.`
          : "The browser blocked clipboard access. Keep the text selected and press Ctrl+Shift+C (⌘C on Mac).",
      );
    }
  }
  function send(sequence: string) {
    connection?.input(sequence);
  }
  // Pointer presses on helper keys must not steal focus from the terminal.
  function keepFocus(event: PointerEvent) {
    event.preventDefault();
  }

  function disposeRenderer() {
    const old = renderer;
    renderer = null;
    term = null;
    if (!old) return;
    // Disposal detaches xterm's element; removing the mount as well keeps any
    // still-scheduled parse of discarded output away from the live document.
    old.t.dispose();
    old.mount.remove();
  }
  function mountRenderer() {
    disposeRenderer();
    if (!modules || !host) return;
    const mount = document.createElement("div");
    mount.className = "h-full w-full";
    host.append(mount);
    const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const t = new modules.xterm.Terminal({
      cursorBlink: !reduced,
      cursorInactiveStyle: "outline",
      fontFamily: MONO,
      fontSize,
      lineHeight: 1.15,
      scrollback: 5000,
      screenReaderMode: true,
      minimumContrastRatio: 4.5,
      disableStdin: phase !== "connected",
      // Remote output must not navigate this tab or open windows.
      linkHandler: { activate: () => {}, allowNonHttpProtocols: false },
      theme: terminalTheme(),
    });
    const fit = new modules.fit.FitAddon();
    t.loadAddon(fit);
    t.open(mount);
    t.attachCustomKeyEventHandler((event) => {
      const combo = event.ctrlKey && event.shiftKey && !event.altKey && !event.metaKey;
      if (combo && event.code === "KeyC") {
        if (event.type === "keydown") {
          event.preventDefault();
          void copySelection();
        }
        return false;
      }
      // Let the browser's paste reach xterm's paste handler.
      if (combo && event.code === "KeyV") return false;
      return true;
    });
    // Keystrokes and parser replies (device queries) only count from the live
    // renderer, never from one holding discarded output.
    t.onData((data) => {
      if (renderer?.t === t) connection?.input(data);
    });
    t.onBinary((data) => {
      if (renderer?.t === t) connection?.binary(data);
    });
    renderer = { t, fit, mount, pending: 0 };
    term = t;
  }

  onMount(() => {
    target = parseTarget(location.search);
    if (!target) {
      missing = true;
      return;
    }
    const pinned = target;
    let disposed = false;
    let unmirror = () => {};
    let unsubscribe = () => {};
    const observer = new ResizeObserver(scheduleFit);
    const online = () => connection?.online();
    const offline = () => connection?.offline();
    const hide = () => connection?.suspend();
    addEventListener("online", online);
    addEventListener("offline", offline);
    addEventListener("pagehide", hide);
    visualViewport?.addEventListener("resize", followViewport);
    visualViewport?.addEventListener("scroll", followViewport);
    if (matchMedia("(max-width: 639px)").matches) fontSize = 13;

    void (async () => {
      const [xterm, fitAddon] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
      if (disposed || !host) return;
      modules = { xterm, fit: fitAddon };
      unmirror = mirrorRuntimeStyles(host);
      mountRenderer();
      const screen = {
        size: () => ({ cols: renderer?.t.cols ?? 100, rows: renderer?.t.rows ?? 30 }),
        write: (data: Uint8Array) => {
          const r = renderer;
          if (!r || r.pending + data.byteLength > MAX_PENDING_OUTPUT) return false;
          r.pending += data.byteLength;
          r.t.write(data, () => (r.pending -= data.byteLength));
          return true;
        },
        clear: () => {
          const focused = !!renderer?.mount.contains(document.activeElement);
          mountRenderer();
          refit();
          if (focused) renderer?.t.focus();
        },
        boundary: () => {
          const time = new Date().toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" });
          // Normalize state left by the old shell without moving over its output.
          // Written through the same ordered queue, so it lands after any old
          // output still being parsed and before the new shell's first bytes.
          //  CAN          aborts an escape/OSC/DCS sequence cut off by the drop.
          //  ESC 7 + ?1049l  saved cursors are per buffer: on the main buffer
          //               this saves and restores the same position (no homing);
          //               from a full-screen app it returns to the main buffer
          //               and its cursor saved on entry, keeping history intact.
          //  CSI ! p      soft reset: scroll region, SGR, charsets, key modes
          //               (does not move the cursor); then mouse/paste off.
          renderer?.t.write(
            "\x18\x1b7\x1b[?1049l\x1b[!p\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?25h\x1b[0m\r\n" +
              `\x1b[35m── Fresh shell · reconnected ${time} · previous session not restored; input was not replayed ──\x1b[0m\r\n`,
          );
        },
      };
      connection = new TerminalConnection(pinned, screen, (state) => (view = state));
      view = connection.state;
      unsubscribe = session.subscribe((value) => connection?.session(value));
      observer.observe(host);
      refit();
      terminalReady = true;
      connection.start();
      renderer?.t.focus();
    })().catch(() => {
      if (!disposed) failed = "The terminal could not load. Reload this tab to try again.";
    });

    return () => {
      disposed = true;
      connection?.dispose();
      connection = null;
      unsubscribe();
      observer.disconnect();
      cancelAnimationFrame(frame);
      clearTimeout(copyTimer);
      removeEventListener("online", online);
      removeEventListener("offline", offline);
      removeEventListener("pagehide", hide);
      visualViewport?.removeEventListener("resize", followViewport);
      visualViewport?.removeEventListener("scroll", followViewport);
      disposeRenderer();
      modules = null;
      unmirror();
    };
  });
</script>

<svelte:head><title>{title}</title></svelte:head>

{#if missing}
  <section class="mx-auto max-w-md px-6 py-16 text-center">
    <span class="mx-auto grid size-11 place-items-center rounded-full bg-base-300 text-base-content/70"
      ><TriangleAlert size={20} aria-hidden="true" /></span
    >
    <h1 class="mt-4 text-lg font-semibold">This console link is incomplete</h1>
    <p class="mt-2 text-sm leading-relaxed text-base-content/70">
      A machine console is pinned to one team, machine, meeting, revision and lab allocation, and this link does not
      name all of them. LabCentral will not guess a replacement. Choose the team and machine again under Coach machine
      access, then select Open SSH.
    </p>
    <a href="/admin/" class="btn btn-sm btn-primary mt-6 min-h-11">Choose a machine</a>
  </section>
{:else}
  <section
    bind:this={workspace}
    class={["flex h-[calc(100dvh-3.5rem)] flex-col bg-base-100", !compact && "min-h-80"]}
    style:height={fitHeight ? `${fitHeight}px` : null}
    aria-labelledby="terminal-title"
  >
    <div class="navbar min-h-0 flex-wrap gap-x-4 gap-y-1.5 border-b border-base-content/10 bg-base-100 px-3 py-1.5 sm:px-4">
      <div class="navbar-start w-auto min-w-52 flex-1 gap-3">
        <SquareTerminal size={20} aria-hidden="true" class="shrink-0 text-primary" />
        <div class="min-w-0 flex-1">
          <h1 id="terminal-title" class="flex min-w-0 items-baseline gap-2 text-sm font-semibold">
            <span class="truncate font-mono">{target?.machine ?? "Machine"}</span><span
              class="shrink-0 text-xs font-normal text-base-content/70">SSH</span
            >
          </h1>
          <div class={["breadcrumbs max-w-full py-0 text-xs text-base-content/70", compact && "hidden"]}>
            <ul>
              <li><a href="/admin/" class="min-h-6">{meetingLabel}</a></li>
              <li>
                <span class="inline-flex items-center gap-1.5"
                  ><Users size={13} aria-hidden="true" />{teamLabel}</span
                >
              </li>
              <li>
                <span class="inline-flex items-center gap-1.5" title={target?.template}
                  ><Server size={13} aria-hidden="true" /><span class="sr-only">Revision </span><span
                    class="max-w-48 truncate font-mono">{target?.template ?? ""}</span
                  ></span
                >
              </li>
            </ul>
          </div>
        </div>
      </div>
      <div class="navbar-end ml-auto w-auto gap-2">
        <span
          class={[
            "inline-flex min-h-11 items-center gap-2 px-1 text-sm",
            phase === "connected" ? "text-success" : ["denied", "exhausted"].includes(phase) ? "text-error" : "",
          ]}
        >
          {#if phase === "authorizing" || phase === "connecting"}<span
              class="loading loading-spinner loading-xs motion-reduce:animate-none"
              aria-hidden="true"
            ></span>
          {:else if phase === "connected"}<CircleCheck size={16} aria-hidden="true" />
          {:else if phase === "retrying"}<RotateCw size={16} aria-hidden="true" />
          {:else if phase === "offline"}<WifiOff size={16} aria-hidden="true" />
          {:else if phase === "ended"}<CircleStop size={16} aria-hidden="true" />
          {:else if phase === "disconnected"}<Unplug size={16} aria-hidden="true" />
          {:else if phase === "denied"}<ShieldX size={16} aria-hidden="true" />
          {:else}<TriangleAlert size={16} aria-hidden="true" />{/if}
          <span role="status" data-terminal-status={phase}>{label}</span>
        </span>
        <button
          type="button"
          class="btn btn-sm min-h-11"
          disabled={!terminalReady}
          onpointerdown={keepFocus}
          aria-label="Copy selection"
          onclick={copySelection}
          ><Copy size={15} aria-hidden="true" /><span class="sm:hidden">Copy</span><span class="max-sm:hidden"
            >Copy selection</span
          ></button
        >
        {#if active}<button
            type="button"
            class="btn btn-sm min-h-11"
            onclick={() => connection?.disconnect()}><Unplug size={15} aria-hidden="true" />Disconnect</button
          >{/if}
        {#if view?.canReconnect && !["authorizing", "connecting", "connected"].includes(phase)}<button
            type="button"
            class={["btn btn-sm min-h-11", !active && "btn-primary"]}
            aria-label={phase === "retrying" || phase === "offline" ? "Reconnect now" : "Reconnect"}
            onclick={() => {
              connection?.reconnect();
              term?.focus();
            }}><RotateCw size={15} aria-hidden="true" />Reconnect</button
          >{/if}
      </div>
    </div>

    {#if failed}
      <div role="alert" class="alert alert-soft alert-error rounded-none">
        <TriangleAlert size={18} aria-hidden="true" /><span>{failed}</span>
      </div>
    {:else if phase === "retrying" || phase === "offline"}
      <div role="status" class="alert alert-soft alert-warning rounded-none">
        {#if phase === "offline"}<WifiOff size={18} aria-hidden="true" />{:else}<RotateCw
            size={18}
            aria-hidden="true"
          />{/if}
        <span class="min-w-0 break-words"
          >{view?.message}
          {phase === "offline"
            ? "You are offline; the console retries when your network returns."
            : `Retry ${view?.attempt} of ${RETRY_DELAYS.length}`}{#if phase === "retrying"}<span
              class="tabular-nums"
              aria-hidden="true">{` in ${seconds} s`}</span
            >{/if}. Reconnecting starts a fresh shell; the previous interactive session is not restored and input is never
          replayed.</span
        >
      </div>
    {:else if phase === "ended" || phase === "disconnected"}
      <div role="status" class="alert alert-soft alert-info rounded-none">
        {#if phase === "ended"}<CircleStop size={18} aria-hidden="true" />{:else}<Unplug
            size={18}
            aria-hidden="true"
          />{/if}<span class="min-w-0 break-words">{view?.message}</span>
      </div>
    {:else if phase === "exhausted" || phase === "denied"}
      <div role="alert" class="alert alert-soft alert-error rounded-none">
        {#if phase === "denied"}<ShieldX size={18} aria-hidden="true" />{:else}<TriangleAlert
            size={18}
            aria-hidden="true"
          />{/if}
        <span class="min-w-0 break-words"
          >{phase === "exhausted"
            ? `Stopped after ${RETRY_DELAYS.length} automatic retries. ${view?.message} Reconnect when the machine is available.`
            : view?.message}</span
        >
        {#if phase === "denied" && !view?.canReconnect}<a href="/admin/" class="btn btn-sm min-h-11"
            >Meeting admin</a
          >{/if}
      </div>
    {/if}
    {#if view?.notice || view?.queued}
      <div role="status" class="alert alert-soft alert-warning rounded-none">
        <TriangleAlert size={18} aria-hidden="true" /><span class="min-w-0 break-words"
          >{view.notice || `Sending ${Math.ceil(view.queued / 1024)} KiB of input while the connection catches up…`}</span
        >
      </div>
    {/if}

    <div class="relative min-h-12 flex-1 overflow-hidden bg-base-100">
      <div
        bind:this={host}
        class="absolute inset-y-1.5 right-0 left-2 sm:left-3"
        role="region"
        aria-label={`SSH terminal for ${target?.machine ?? "machine"} on ${teamLabel}`}
      ></div>
      {#if !terminalReady && !failed}
        <div class="absolute inset-0 flex items-center justify-center gap-3 text-sm text-base-content/70">
          <span class="loading loading-spinner loading-sm motion-reduce:animate-none" aria-hidden="true"></span>Loading
          terminal…
        </div>
      {/if}
    </div>

    <div
      class="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-base-content/10 bg-base-100 px-3 py-1.5 pb-[max(0.375rem,env(safe-area-inset-bottom))] sm:px-4"
    >
      <div class="join" role="group" aria-label="Send keys">
        <button
          type="button"
          class="btn btn-sm join-item min-h-11 px-2.5"
          aria-label="Send Ctrl+C"
          disabled={phase !== "connected"}
          onpointerdown={keepFocus}
          onclick={() => send("\x03")}><kbd class="kbd kbd-sm">Ctrl</kbd><kbd class="kbd kbd-sm">C</kbd></button
        >
        <button
          type="button"
          class="btn btn-sm join-item min-h-11 px-2.5"
          aria-label="Send Escape"
          disabled={phase !== "connected"}
          onpointerdown={keepFocus}
          onclick={() => send("\x1b")}><kbd class="kbd kbd-sm">Esc</kbd></button
        >
        <button
          type="button"
          class="btn btn-sm join-item min-h-11 px-2.5"
          aria-label="Send Tab"
          disabled={phase !== "connected"}
          onpointerdown={keepFocus}
          onclick={() => send("\t")}><kbd class="kbd kbd-sm">Tab</kbd></button
        >
      </div>
        <div class={["join", compact && "hidden"]} role="group" aria-label="Terminal text size">
          <button
            type="button"
            class="btn btn-sm join-item min-h-11 min-w-11 px-2"
            aria-label="Smaller text"
            disabled={fontSize <= 10}
            onclick={() => setFont(fontSize - 1)}><AArrowDown size={16} aria-hidden="true" /></button
          >
          <output
            class="join-item flex min-h-11 min-w-12 items-center justify-center border-y border-base-content/10 bg-base-200 px-2 text-xs tabular-nums">{fontSize} px</output
          >
          <button
            type="button"
            class="btn btn-sm join-item min-h-11 min-w-11 px-2"
            aria-label="Larger text"
            disabled={fontSize >= 24}
            onclick={() => setFont(fontSize + 1)}><AArrowUp size={16} aria-hidden="true" /></button
          >
        </div>
      <p class={["hidden items-center gap-1.5 text-xs text-base-content/70", !compact && "md:flex"]}>
        <kbd class="kbd kbd-sm">Ctrl</kbd><kbd class="kbd kbd-sm">Shift</kbd><kbd class="kbd kbd-sm">C</kbd> copies,
        <kbd class="kbd kbd-sm">Ctrl</kbd><kbd class="kbd kbd-sm">Shift</kbd><kbd class="kbd kbd-sm">V</kbd> pastes
      </p>
      <p
        class={[
          "flex min-h-6 basis-full items-center gap-1.5 text-xs text-base-content/70 xl:ml-auto xl:basis-auto",
          compact && !copyNote && "hidden",
        ]}
        role="status"
        aria-live="polite"
      >
        {#if copyNote}{copyNote}{:else if renews}<Timer size={14} aria-hidden="true" />Fresh shell at {renews}; reconnects
          automatically{:else}Reconnecting always starts a fresh shell{/if}
      </p>
    </div>
  </section>
{/if}
