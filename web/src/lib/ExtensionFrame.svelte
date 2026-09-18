<script lang="ts">
  import { featurePermissions, featureRequest } from "./extension-bridge";
  import { presentAccess } from "./access-navigation";
  import { onMount } from "svelte";
  import { goto, beforeNavigate } from "$app/navigation";
  import { api, mutationHeaders, downloadAccessConfiguration, type AccessLaunch } from "./api";
  import type { components } from "./api.generated";
  type Extension = components["schemas"]["BrowserExtensionDTO"];
  type Topology = components["schemas"]["Topology"];
  let { extension, instanceId = "", resourceId = "", initialSessionId = "", projectId = "" }: { extension: Extension; instanceId?: string; resourceId?:string; initialSessionId?:string; projectId?:string } = $props();
  let frame = $state<HTMLIFrameElement>();
  let error = $state("");
  let disconnect = () => {};
  beforeNavigate(() => disconnect());
  onMount(() => {
    const nonce = crypto.randomUUID();
    const abort = new AbortController();
    let channel: MessageChannel | undefined;
    let initialUsed=false;
    let ready = false, pending = 0, requests = 0, connected = false;
    const streams = new Map<string, { socket: WebSocket; sessionId: string; ready: boolean; received: number }>();
    const closeStreams = () => { for (const stream of streams.values()) stream.socket.close(); streams.clear(); };
    const permissions = new Set(extension.permissions);
    const required: Record<string, string> = {
      ...featurePermissions,
      "access.stream.write": "access.stream", "access.stream.close": "access.stream",
      "context.read": "context.read", "topology.read": "topology.read",
      "status.read": "status.read", "navigation.lab": "navigation.lab", "access.create": "access.create",
    };
    const reset = setInterval(() => { requests = 0; for (const stream of streams.values()) stream.received = 0; }, 1000);
    const connect = () => {
      if (connected) { closeStreams(); abort.abort(); channel?.port1.close(); channel?.port2.close(); error = "Extension navigation ended its connection."; return; }
      connected = true;
      channel?.port1.close(); channel?.port2.close(); ready = false;
      channel = new MessageChannel();
      channel.port1.onmessage = async (event: MessageEvent) => {
        if (abort.signal.aborted) return;
        const data = event.data;
        if (!data || data.version !== 1 || typeof data.id !== "string" || data.id.length > 128 || typeof data.method !== "string") return;
        const port = channel!.port1;
        if (data.method === "ready" && data.nonce === nonce) { ready = true; return; }
        if (!ready || ++requests > 20 || pending >= 4 || !permissions.has(required[data.method])) {
          port.postMessage({ version: 1, id: data.id, error: "Request is not permitted" }); return;
        }
        pending++;
        try {
          if (!(data.data instanceof ArrayBuffer) && JSON.stringify(data).length > (12 << 20)) throw new Error("Request exceeds limit");
          let result: unknown;
          if (data.method === "context.read") result = { instanceId, resourceId, projectId, pluginInstance: extension.pluginInstance, hostVersion: 2, methods: Object.keys(required).filter(method => permissions.has(required[method])) };
          else if (featurePermissions[data.method]) {
            if (extension.slot !== "page") throw new Error("Feature operation requires a page contribution");
            const request = featureRequest(data.method, data.input, {projectId});
            result = await api(request.path, {method: request.method, signal: abort.signal,
              headers: request.method === "POST" ? {...mutationHeaders(), "Idempotency-Key": request.idempotencyKey!} : undefined,
              body: request.body ? JSON.stringify(request.body) : undefined});
          }
          else if (data.method === "topology.read" || data.method === "status.read") {
            if (!instanceId) throw new Error("No lab context");
            result = await api<Topology>(`/lab-instances/${encodeURIComponent(instanceId)}/topology`, { signal: abort.signal });
          } else if (data.method === "navigation.lab") {
            if (!instanceId) throw new Error("No lab context");
            await goto(`/lab?id=${encodeURIComponent(instanceId)}`); result = {};
          } else if (data.method === "access.create") {
            if (resourceId && data.resourceId!==resourceId) throw new Error("Resource outside frame context");
            if (!instanceId || typeof data.resourceId !== "string" || typeof data.accessMethod !== "string") throw new Error("Invalid access request");
            const topology = await api<Topology>(`/lab-instances/${encodeURIComponent(instanceId)}/topology`, { signal: abort.signal });
            const action = topology.resources?.find(r => r.id === data.resourceId)?.actions?.find(a => a.method === data.accessMethod);
            if (!action) throw new Error("Access is unavailable");
            let launch:AccessLaunch;
            const initialToken=!initialUsed&&initialSessionId?sessionStorage.getItem(`labcentral.access-token.v1.${initialSessionId}`):null;
            if(initialToken){initialUsed=true;sessionStorage.removeItem(`labcentral.access-token.v1.${initialSessionId}`);const session=await api<AccessLaunch["session"]>(`/access-sessions/${encodeURIComponent(initialSessionId)}`,{signal:abort.signal});if(session.instanceId!==instanceId||session.resourceId!==data.resourceId||session.method!==action.method)throw new Error("Session outside frame context");launch={session,oneTimeToken:initialToken,launchUrl:""};}
            else launch = await api<AccessLaunch>("/access-sessions", { method: "POST", signal: abort.signal, headers: mutationHeaders(), body: JSON.stringify({ instanceId, resourceId: data.resourceId, method: action.method, durationSeconds: 900 }) });
            if (action.presentation === "credential-download") {
              const blob = await downloadAccessConfiguration(launch.session.id, launch.oneTimeToken, abort.signal);
              if (abort.signal.aborted) return;
              const url = URL.createObjectURL(blob); const link = document.createElement("a"); link.href = url; link.download = "lab-access.conf"; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
            } else if (permissions.has("access.stream")) {
              if (streams.size >= 2) throw new Error("Stream limit reached");
              const presentation = await api<{streamPath?:string}>(`/access-sessions/${encodeURIComponent(launch.session.id)}/presentation`, {signal:abort.signal});
              if (!presentation.streamPath?.startsWith("/") || presentation.streamPath.startsWith("//")) throw new Error("Stream unavailable");
              const handle = crypto.randomUUID();
              const socket = new WebSocket(`${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${presentation.streamPath}`, ["binary"]);
              socket.binaryType = "arraybuffer";
              streams.set(handle, {socket, sessionId:launch.session.id, ready:false, received:0});
              const timeout = setTimeout(() => socket.close(), 10000);
              socket.onopen = () => { if (abort.signal.aborted) {socket.close();return;} socket.send(JSON.stringify({accessSessionId:launch.session.id,oneTimeToken:launch.oneTimeToken})); };
              let authenticated = false;
              socket.onmessage = event => {
                if (abort.signal.aborted) {socket.close();return;}
                if (!authenticated) {
                  if (typeof event.data !== "string") {socket.close();return;}
                  try { const status=JSON.parse(event.data); if(status.authorized !== true) {socket.close();return;} } catch {socket.close();return;}
                  authenticated=true; const stream=streams.get(handle); if(stream) stream.ready=true; clearTimeout(timeout); port.postMessage({version:1,type:"stream.open",handle}); return;
                }
                if (!(event.data instanceof ArrayBuffer) || event.data.byteLength>65536) {socket.close();return;}
                const stream=streams.get(handle); if(!stream || (stream.received += event.data.byteLength)>262144) {socket.close();return;}
                port.postMessage({version:1,type:"stream.data",handle,data:event.data},[event.data]);
              };
              socket.onclose = () => { clearTimeout(timeout);streams.delete(handle);if(!abort.signal.aborted)port.postMessage({version:1,type:"stream.closed",handle}); };
              result = { sessionId:launch.session.id, handle };
            } else {
              await presentAccess(launch,abort.signal);
            }
            result ??= { sessionId: launch.session.id };
          } else if (data.method === "access.stream.write" || data.method === "access.stream.close") {
            const stream=streams.get(data.handle);
            if (!stream) throw new Error("Invalid stream handle");
            if (data.method === "access.stream.close") {stream.socket.close();streams.delete(data.handle);}
            else {
              if (!stream.ready || !(data.data instanceof ArrayBuffer) || data.data.byteLength>65536 || stream.socket.readyState!==WebSocket.OPEN || stream.socket.bufferedAmount>262144) throw new Error("Invalid stream write");
              stream.socket.send(data.data);
            }
            result={};
          } else throw new Error("Unsupported method");
          if (!abort.signal.aborted) port.postMessage({ version: 1, id: data.id, result });
        } catch {
          if (!abort.signal.aborted) port.postMessage({ version: 1, id: data.id, error: "Extension request failed" });
        } finally { pending--; }
      };
      // The sandbox has an opaque origin. The port is transferred to this exact
      // frame only; subsequent requests cannot arrive through global messages.
      frame?.contentWindow?.postMessage({ version: 1, type: "labcentral.connect", nonce }, "*", [channel.port2]);
    };
    frame?.addEventListener("load", connect);
    const timer = setTimeout(() => { if (!ready) error = "Extension did not connect."; }, 15000);
    disconnect = () => { closeStreams(); abort.abort(); channel?.port1.close(); channel?.port2.close(); };
    return () => { closeStreams(); abort.abort(); clearInterval(reset); clearTimeout(timer); channel?.port1.close(); channel?.port2.close(); frame?.removeEventListener("load", connect); };
  });
</script>
{#if error}<p role="alert" class="text-error">{error}</p>{/if}
<iframe bind:this={frame} src={extension.url} title={extension.title} sandbox="allow-scripts" referrerpolicy="no-referrer" class="w-full rounded-box border border-base-300" style:height={extension.slot === "page" ? "75vh" : "24rem"}></iframe>
