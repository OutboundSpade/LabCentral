import type { ITheme } from "@xterm/xterm";
import type { CoachMachines, Machine, Overview } from "./types";

// Coach SSH runs in its own tab. The URL names the pinned allocation only;
// credentials, CSRF tokens and terminal output never enter URLs or storage.
export const TERMINAL_PATH = "/admin/terminal/";
const KEYS = ["team", "machine", "meeting", "template", "instance"] as const;

// `instance` is the opaque library allocation ID of the team's lab. A console
// never follows a different allocation, even for the same team and revision.
export interface TerminalTarget {
  team: string;
  machine: string;
  meeting: string;
  template: string;
  instance: string;
}
export type TerminalInventory = CoachMachines;

function identifier(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 200 &&
    !/[\s\u0000-\u001f\u007f]/.test(value)
  );
}

export function terminalURL(target: TerminalTarget) {
  const query = new URLSearchParams();
  for (const key of KEYS) query.set(key, target[key]);
  return `${TERMINAL_PATH}?${query}`;
}

export function parseTarget(search: string): TerminalTarget | null {
  const query = new URLSearchParams(search);
  const values = KEYS.map((key) => query.getAll(key));
  if (values.some((v) => v.length !== 1 || !identifier(v[0]))) return null;
  const [team, machine, meeting, template, instance] = values.map((v) => v[0]);
  return { team, machine, meeting, template, instance };
}

export function parseInventory(value: unknown): TerminalInventory | null {
  if (!value || typeof value !== "object") return null;
  const v = value as Record<string, unknown>;
  // IDs pin the allocation and must be present; names are display-only and may be empty.
  if (!["teamID", "meetingID", "template", "instanceID"].every((key) => identifier(v[key]))) return null;
  if (typeof v.teamName !== "string" || typeof v.meetingName !== "string") return null;
  if (!Array.isArray(v.machines)) return null;
  const machines: Machine[] = [];
  for (const m of v.machines as unknown[]) {
    const item = m as Record<string, unknown> | null;
    if (!item || !identifier(item.name) || typeof item.status !== "string" || typeof item.desktop !== "boolean") return null;
    machines.push({ name: item.name, status: item.status, desktop: item.desktop });
  }
  return {
    teamID: v.teamID as string,
    teamName: v.teamName as string,
    meetingID: v.meetingID as string,
    meetingName: v.meetingName as string,
    template: v.template as string,
    instanceID: v.instanceID as string,
    machines,
  };
}

// Why the shared session no longer authorizes this console, if it does not.
// Identity and pinned-target changes are permanent; an apparently expired
// meeting may be a clock difference, so only a manual reauthorization remains.
function meetingProblem(value: Overview, target: TerminalTarget) {
  const m = value.meeting;
  if (!m || m.id !== target.meeting)
    return { final: true, message: "The meeting this console was opened for is no longer current. Its transcript was cleared." };
  if (m.template !== target.template)
    return { final: true, message: "The meeting's revision changed. This console stays pinned to its original revision, so it stopped and cleared its transcript." };
  if (m.status === "ending" || m.status === "ended")
    return { final: true, message: "The meeting ended, so the console closed and its transcript was cleared." };
  const expires = Date.parse(m.expires);
  if (Number.isFinite(expires) && expires <= Date.now())
    return { final: false, message: "The meeting's time is up, so the console stopped and its transcript was cleared. Reconnect only works if time was added." };
  return null;
}

export function clampSize(size: { cols: number; rows: number }) {
  const bound = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.floor(n) || lo));
  return { cols: bound(size.cols, 2, 500), rows: bound(size.rows, 2, 200) };
}

// Text input is split at code point boundaries: 2048 UTF-16 units encode to at
// most 6 KiB, below the server's 16 KiB frame limit.
export function textChunks(data: string): Uint8Array[] {
  const encoder = new TextEncoder();
  const chunks: Uint8Array[] = [];
  for (let start = 0; start < data.length; ) {
    let end = Math.min(data.length, start + 2048);
    const last = data.charCodeAt(end - 1);
    if (end < data.length && last >= 0xd800 && last <= 0xdbff) end--;
    chunks.push(encoder.encode(data.slice(start, end)));
    start = end;
  }
  return chunks;
}
// xterm reports some mouse protocols as byte strings (one char per byte).
export function byteChunks(data: string): Uint8Array[] {
  const chunks: Uint8Array[] = [];
  for (let start = 0; start < data.length; start += 8192)
    chunks.push(Uint8Array.from(data.slice(start, start + 8192), (c) => c.charCodeAt(0) & 0xff));
  return chunks;
}

// The renderer needs concrete sRGB values; resolve the active daisyUI tokens.
export function terminalTheme(): ITheme {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  const styles = getComputedStyle(document.documentElement);
  const rgb = (token: string) => {
    const value = styles.getPropertyValue(`--color-${token}`).trim();
    if (!context || !value) return null;
    context.clearRect(0, 0, 1, 1);
    context.fillStyle = "#000";
    context.fillStyle = value;
    context.fillRect(0, 0, 1, 1);
    const [r, g, b] = context.getImageData(0, 0, 1, 1).data;
    return [r, g, b] as const;
  };
  const hex = (token: string) => {
    const c = rgb(token);
    return c ? `#${c.map((n) => n.toString(16).padStart(2, "0")).join("")}` : undefined;
  };
  const primary = rgb("primary");
  return {
    background: hex("base-100"),
    foreground: hex("base-content"),
    cursor: hex("primary"),
    cursorAccent: hex("base-100"),
    selectionBackground: primary ? `rgba(${primary.join(",")},0.35)` : undefined,
    black: hex("base-300"),
    red: hex("error"),
    green: hex("success"),
    yellow: hex("warning"),
    blue: hex("info"),
    magenta: hex("primary"),
    white: hex("base-content"),
  };
}

// The app CSP allows only same-origin style sheets, which blocks the <style>
// elements xterm generates at runtime. Mirror that vendor CSS into a
// constructed sheet (CSSOM is not an inline style) scoped to this terminal.
export function mirrorRuntimeStyles(root: HTMLElement) {
  if (typeof CSSStyleSheet === "undefined" || !("adoptedStyleSheets" in document)) return () => {};
  const sheet = new CSSStyleSheet();
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  let last = "";
  const sync = () => {
    const css = [...root.querySelectorAll("style")].map((s) => s.textContent ?? "").join("\n");
    if (css !== last) sheet.replaceSync((last = css));
  };
  const observer = new MutationObserver(sync);
  observer.observe(root, { subtree: true, childList: true, characterData: true });
  return () => {
    observer.disconnect();
    document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => s !== sheet);
  };
}

export type Phase =
  | "authorizing"
  | "connecting"
  | "connected"
  | "retrying"
  | "offline"
  | "ended"
  | "disconnected"
  | "exhausted"
  | "denied";
export interface TerminalState {
  phase: Phase;
  message: string;
  attempt: number;
  retryAt: number;
  expires: string;
  inventory: TerminalInventory | null;
  canReconnect: boolean;
  queued: number;
  notice: string;
}
export interface TerminalScreen {
  size(): { cols: number; rows: number };
  // False when the renderer's bounded output backlog is full.
  write(data: Uint8Array): boolean;
  // Discard the transcript and any output still queued for rendering.
  clear(): void;
  boundary(): void;
}

export const RETRY_DELAYS = [1000, 2000, 4000, 8000, 16000];
export const FETCH_TIMEOUT = 10000;
export const OPEN_TIMEOUT = 10000;
export const READY_TIMEOUT = 30000;
// A connection must stay up this long before the retry budget is restored,
// so a server that accepts and immediately drops cannot cause endless retries.
export const STABLE_AFTER = 30000;
const HIGH_WATER = 64 * 1024;
const MAX_QUEUED = 256 * 1024;

class Transient extends Error {}
type Timer = "retry" | "open" | "ready" | "stable" | "pump" | "resize";

function reasonText(reason: string) {
  return reason.replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, 200);
}

// One controller per tab. Each attempt owns a generation; any timer, fetch or
// socket event from an older generation is ignored, so late work can never
// connect, write or paint after Disconnect, a target change or disposal.
export class TerminalConnection {
  state: TerminalState = {
    phase: "authorizing",
    message: "",
    attempt: 0,
    retryAt: 0,
    expires: "",
    inventory: null,
    canReconnect: false,
    queued: 0,
    notice: "",
  };
  #generation = 0;
  #socket: WebSocket | null = null;
  #abort: AbortController | null = null;
  #timers = { retry: 0, open: 0, ready: 0, stable: 0, pump: 0, resize: 0 };
  #retries = 0;
  #shells = 0;
  #owner = "";
  #queue: Uint8Array[] = [];
  #queuedBytes = 0;
  #sentSize = "";
  #disposed = false;
  // Identity changes and missing permission end the tab for good.
  #final = false;

  constructor(
    readonly target: TerminalTarget,
    private screen: TerminalScreen,
    private publish: (state: TerminalState) => void,
  ) {}

  #set(patch: Partial<TerminalState>) {
    this.state = { ...this.state, ...patch };
    this.publish(this.state);
  }
  #clear(name: Timer) {
    clearTimeout(this.#timers[name]);
    this.#timers[name] = 0;
  }
  // Stop everything owned by the current attempt. Input is never kept.
  #halt() {
    this.#generation++;
    this.#abort?.abort();
    this.#abort = null;
    for (const name of Object.keys(this.#timers) as Timer[]) this.#clear(name);
    const socket = this.#socket;
    this.#socket = null;
    if (socket) {
      socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
      if (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN) socket.close(1000);
    }
    this.#queue = [];
    this.#queuedBytes = 0;
  }

  start() {
    if (this.#disposed) return;
    this.#retries = 0;
    void this.#run("Checking your administrator session…");
  }
  // Explicit reset from any state: drop the current shell, restore the retry
  // budget and authorize a fresh one for the same pinned allocation.
  reconnect() {
    if (this.#disposed || this.#final) return;
    this.#retries = 0;
    void this.#run("Starting a fresh shell…");
  }
  disconnect() {
    if (this.#disposed || this.#final) return;
    this.#halt();
    this.#set({
      phase: "disconnected",
      message: "You disconnected. Automatic reconnection is off until you choose Reconnect.",
      canReconnect: true,
      retryAt: 0,
      queued: 0,
    });
  }
  // pagehide: release the socket; a restored tab waits for an explicit Reconnect.
  suspend() {
    if (this.#disposed || this.#final || ["denied", "ended", "disconnected"].includes(this.state.phase)) return;
    this.#halt();
    this.#set({
      phase: "disconnected",
      message: "This tab was hidden, so the shell was closed. Reconnect to start a fresh shell.",
      canReconnect: true,
      retryAt: 0,
      queued: 0,
    });
  }
  dispose() {
    this.#disposed = true;
    this.#halt();
  }
  // Resumes only a pending automatic retry; stopped, denied and manually
  // disconnected consoles stay as they are.
  online() {
    if (this.#disposed || this.#final || !["offline", "retrying"].includes(this.state.phase)) return;
    void this.#run("Network restored. Starting a fresh shell…");
  }
  // A socket can stay OPEN after the network disappears. Close it, drop any
  // unsent input and spend one retry, so flapping networks stay bounded.
  offline() {
    if (this.#disposed || this.#final) return;
    if (this.state.phase === "retrying") {
      this.#clear("retry");
      return this.#set({ phase: "offline", retryAt: 0 });
    }
    if (["authorizing", "connecting", "connected"].includes(this.state.phase))
      this.#retry("This device went offline, so the shell connection was closed. Unsent input was discarded.", true);
  }
  // Shared session updates from the app shell: any confirmed identity or
  // meeting change ends this console and clears its transcript.
  session(value: Overview | null) {
    if (this.#disposed || !this.#owner || this.#final) return;
    if (!value)
      return this.#deny("You signed out, so the console closed and its transcript was cleared.", true, false);
    if (value.user.id !== this.#owner)
      return this.#deny(
        "This browser is now signed in as a different account. The console closed and its transcript was cleared.",
        true,
        false,
      );
    if (!value.user.admin)
      return this.#deny("This account is no longer a meeting administrator. The console closed and its transcript was cleared.", true, false);
    const problem = meetingProblem(value, this.target);
    // A non-final problem stays stopped; repeated polls do not re-clear it.
    if (problem && !(this.state.phase === "denied" && !problem.final)) this.#deny(problem.message, true, !problem.final);
  }

  #deny(message: string, clear: boolean, canReconnect: boolean) {
    this.#halt();
    if (!canReconnect) this.#final = true;
    if (clear) {
      this.screen.clear();
      this.#shells = 0;
    }
    this.#set({
      phase: "denied",
      message,
      canReconnect,
      retryAt: 0,
      expires: "",
      queued: 0,
      notice: "",
      ...(clear ? { inventory: null } : {}),
    });
  }
  #retry(message: string, offline = !navigator.onLine) {
    this.#halt();
    if (this.#retries >= RETRY_DELAYS.length) {
      this.#set({
        phase: "exhausted",
        message,
        canReconnect: true,
        retryAt: 0,
        expires: "",
        queued: 0,
      });
      return;
    }
    const delay = RETRY_DELAYS[this.#retries++];
    const base = { message, attempt: this.#retries, canReconnect: true, expires: "", queued: 0 };
    if (offline) return this.#set({ ...base, phase: "offline", retryAt: 0 });
    this.#timers.retry = window.setTimeout(() => {
      this.#timers.retry = 0;
      void this.#run("Starting a fresh shell…");
    }, delay);
    this.#set({ ...base, phase: "retrying", retryAt: Date.now() + delay });
  }

  async #get(path: string, abort: AbortController) {
    const timer = setTimeout(() => abort.abort(), FETCH_TIMEOUT);
    try {
      const response = await fetch(path, { cache: "no-store", credentials: "same-origin", signal: abort.signal });
      let body: unknown = null;
      try {
        body = await response.json();
      } catch {
        body = null;
      }
      return { status: response.status, body };
    } catch {
      throw new Transient("LabCentral did not respond. Check your network connection.");
    } finally {
      clearTimeout(timer);
    }
  }

  async #run(message: string) {
    if (this.#disposed || this.#final) return;
    this.#halt();
    const generation = this.#generation;
    const live = () => generation === this.#generation && !this.#disposed;
    const abort = new AbortController();
    this.#abort = abort;
    const { team, machine, meeting, template, instance } = this.target;
    this.#set({ phase: "authorizing", message, retryAt: 0, expires: "", queued: 0, notice: "", canReconnect: true });
    let csrf: string;
    let inventory: TerminalInventory;
    try {
      const me = await this.#get("/api/me", abort);
      if (!live()) return;
      if (me.status === 401)
        return this.#deny("Your sign-in ended. Sign in again, then open SSH from the meeting page.", true, false);
      if (me.status !== 200) throw new Transient("LabCentral could not confirm your sign-in.");
      const who = me.body as Overview | null;
      if (!who?.user || typeof who.user.id !== "string" || typeof who.csrf !== "string" || !who.csrf)
        throw new Transient("LabCentral returned an incomplete session.");
      if (this.#owner && who.user.id !== this.#owner)
        return this.#deny(
          "This browser is now signed in as a different account. The console closed and its transcript was cleared.",
          true,
          false,
        );
      if (!who.user.admin)
        return this.#deny("Only meeting administrators can open machine consoles.", true, false);
      this.#owner = who.user.id;
      const problem = meetingProblem(who, this.target);
      if (problem) return this.#deny(`${problem.message} Open SSH again from the meeting page.`, true, !problem.final);
      csrf = who.csrf;
      const list = await this.#get(`/api/admin/teams/${encodeURIComponent(team)}/machines`, abort);
      if (!live()) return;
      if (list.status === 401 || list.status === 403)
        return this.#deny("Your administrator access was not accepted. Sign in again as a meeting administrator.", true, false);
      if ([400, 404, 409, 410].includes(list.status)) {
        const reason = (list.body as { error?: unknown } | null)?.error;
        return this.#deny(
          `${typeof reason === "string" ? reasonText(reason) + " " : ""}This console stays pinned to its team and revision, so it stopped.`,
          true,
          true,
        );
      }
      if (list.status !== 200) throw new Transient("The machine list is temporarily unavailable.");
      const parsed = parseInventory(list.body);
      if (!parsed) throw new Transient("The machine list was incomplete.");
      if (
        parsed.teamID !== team ||
        parsed.meetingID !== meeting ||
        parsed.template !== template ||
        parsed.instanceID !== instance
      )
        return this.#deny(
          "The team's lab allocation, meeting or revision changed. This console never follows a different allocation, so it stopped and cleared its transcript. Open SSH again from the meeting page.",
          true,
          false,
        );
      const found = parsed.machines.find((m) => m.name === machine);
      if (!found)
        return this.#deny(`${machine} is not an SSH machine in this revision.`, true, false);
      inventory = parsed;
      this.#set({ inventory });
      if (found.status !== "ready") throw new Transient(`${machine} is ${found.status || "not ready"}.`);
    } catch (e) {
      if (!live()) return;
      return this.#retry(e instanceof Transient ? e.message : "The console could not be prepared.");
    }
    this.#open(generation, csrf, inventory);
  }

  #open(generation: number, csrf: string, inventory: TerminalInventory) {
    const { team, machine, meeting, template, instance } = this.target;
    const url = new URL(
      `/api/admin/teams/${encodeURIComponent(team)}/machines/${encodeURIComponent(machine)}/terminal`,
      location.href,
    );
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    let socket: WebSocket;
    try {
      socket = new WebSocket(url);
    } catch {
      return this.#retry("The browser could not open the console connection.");
    }
    socket.binaryType = "arraybuffer";
    this.#socket = socket;
    const current = () => generation === this.#generation && this.#socket === socket;
    this.#set({ phase: "connecting", message: `Starting a shell on ${machine}…` });
    this.#timers.open = window.setTimeout(() => {
      if (current()) this.#retry("The console did not answer within 10 seconds.");
    }, OPEN_TIMEOUT);
    socket.onopen = () => {
      if (!current()) return;
      this.#clear("open");
      const { cols, rows } = clampSize(this.screen.size());
      this.#sentSize = `${cols}x${rows}`;
      socket.send(JSON.stringify({ csrf, cols, rows, meetingID: meeting, template, instanceID: instance }));
      this.#timers.ready = window.setTimeout(() => {
        if (current()) this.#retry(`${machine} did not start a shell within 30 seconds.`);
      }, READY_TIMEOUT);
    };
    socket.onmessage = (event) => {
      if (!current()) return;
      if (typeof event.data === "string") {
        let frame: { type?: unknown; expires?: unknown } | null = null;
        try {
          frame = JSON.parse(event.data);
        } catch {
          return;
        }
        if (frame?.type !== "ready" || this.state.phase !== "connecting") return;
        this.#clear("ready");
        if (this.#shells++ > 0) this.screen.boundary();
        this.#set({
          phase: "connected",
          message: "",
          inventory,
          expires: typeof frame.expires === "string" ? frame.expires : "",
          attempt: this.#retries,
        });
        this.#timers.stable = window.setTimeout(() => {
          if (!current()) return;
          this.#retries = 0;
          this.#set({ attempt: 0 });
        }, STABLE_AFTER);
        this.#sendResize();
        return;
      }
      if (this.state.phase === "connected" && event.data instanceof ArrayBuffer && !this.screen.write(new Uint8Array(event.data)))
        this.#overflow();
    };
    socket.onclose = (event) => {
      if (current()) this.#closed(event.code, reasonText(event.reason));
    };
  }

  // The shell outran the renderer. Close it rather than queue without bound;
  // output already accepted stays, and nothing is replayed on reconnect.
  #overflow() {
    this.#halt();
    this.#set({
      phase: "disconnected",
      message:
        "The shell sent output faster than this tab could display it, so the connection was closed. Output already received stays visible. Reconnect to start a fresh shell; input is never replayed.",
      canReconnect: true,
      retryAt: 0,
      expires: "",
      queued: 0,
    });
  }

  #closed(code: number, reason: string) {
    if (this.#final || this.#disposed) return;
    const { machine } = this.target;
    if (code === 1000) {
      this.#halt();
      return this.#set({
        phase: "ended",
        message: `The shell on ${machine} exited. Start a fresh shell when you need it again.`,
        canReconnect: true,
        retryAt: 0,
        expires: "",
        queued: 0,
      });
    }
    if (code === 1008)
      return this.#deny(
        `${reason || "The server rejected this console request."} Reconnect checks your session again.`,
        false,
        true,
      );
    if (code === 4003)
      return this.#deny(
        `${reason || "Console access ended."} The transcript was cleared. Reconnect only works if the same meeting and team are still available.`,
        true,
        true,
      );
    if (code === 4000) {
      // Ten-minute renewal: a fresh, freshly authorized shell. It still spends
      // the retry budget unless the previous shell stayed up long enough.
      if (this.#retries >= RETRY_DELAYS.length) return this.#retry(reason || "Session limit reached.");
      this.#retries++;
      void this.#run("Session renewed. Starting a fresh shell…");
      return;
    }
    if (code > 4000 && code < 5000)
      return this.#deny(`${reason || "The console closed unexpectedly."}`, false, true);
    this.#retry(reason || "The console connection was lost.");
  }

  input(data: string) {
    return this.#enqueue(textChunks(data));
  }
  binary(data: string) {
    return this.#enqueue(byteChunks(data));
  }
  #enqueue(chunks: Uint8Array[]) {
    const socket = this.#socket;
    if (this.state.phase !== "connected" || !socket || socket.readyState !== WebSocket.OPEN) return false;
    const bytes = chunks.reduce((n, c) => n + c.byteLength, 0);
    if (this.#queuedBytes + bytes > MAX_QUEUED) {
      this.#set({
        notice:
          bytes > MAX_QUEUED
            ? "That paste is larger than 256 KiB, so none of it was sent. Paste it in smaller parts."
            : "Input was not sent because earlier input is still being delivered. Wait for it to finish, then try again.",
      });
      return false;
    }
    if (this.state.notice) this.#set({ notice: "" });
    this.#queue.push(...chunks);
    this.#queuedBytes += bytes;
    this.#pump();
    return true;
  }
  #pump() {
    this.#clear("pump");
    const socket = this.#socket;
    if (!socket || socket.readyState !== WebSocket.OPEN || this.state.phase !== "connected") {
      this.#queue = [];
      this.#queuedBytes = 0;
    }
    while (socket && this.#queue.length && socket.bufferedAmount < HIGH_WATER) {
      const chunk = this.#queue.shift()!;
      this.#queuedBytes -= chunk.byteLength;
      socket.send(chunk);
    }
    if (this.#queue.length) this.#timers.pump = window.setTimeout(() => this.#pump(), 30);
    // Visible backpressure: bytes held locally while the socket drains.
    if (this.#queuedBytes !== this.state.queued) this.#set({ queued: this.#queuedBytes });
  }
  resize() {
    this.#clear("resize");
    this.#timers.resize = window.setTimeout(() => this.#sendResize(), 120);
  }
  #sendResize() {
    this.#clear("resize");
    const socket = this.#socket;
    if (this.state.phase !== "connected" || !socket || socket.readyState !== WebSocket.OPEN) return;
    const { cols, rows } = clampSize(this.screen.size());
    if (`${cols}x${rows}` === this.#sentSize) return;
    this.#sentSize = `${cols}x${rows}`;
    socket.send(JSON.stringify({ type: "resize", cols, rows }));
  }
}
