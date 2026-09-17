<script lang="ts">
  import { api, mutationHeaders } from "./api";
  type Request = {
    clientName: string;
    clientId: string;
    state: string;
    expiresAt: string;
    scope: {
      projectId: string;
      actions: string[];
      templateIds: string[];
      instanceIds: string[];
      targetIds: string[];
    };
  };
  let code = $state(""),
    busy = $state(false),
    error = $state(""),
    message = $state("");
  let request = $state<Request>();
  async function inspect() {
    busy = true;
    error = "";
    message = "";
    request = undefined;
    try {
      request = await api<Request>("/device/inspect", {
        method: "POST",
        headers: mutationHeaders(),
        body: JSON.stringify({ userCode: code }),
      });
    } catch (e) {
      error = e instanceof Error ? e.message : "Code unavailable";
    } finally {
      busy = false;
    }
  }
  async function approve(approve: boolean) {
    busy = true;
    error = "";
    try {
      await api("/device/approve", {
        method: "POST",
        headers: mutationHeaders(),
        body: JSON.stringify({ userCode: code, approve }),
      });
      request = undefined;
      code = "";
      message = approve
        ? "Approved. Return to the application to continue."
        : "Request denied.";
    } catch (e) {
      error = e instanceof Error ? e.message : "Approval failed";
    } finally {
      busy = false;
    }
  }
</script>

<section class="max-w-2xl space-y-5 rounded-box border border-base-300 p-6">
  <h2 class="text-lg font-semibold">Connect an application</h2>
  <p>
    Enter the code displayed by the application you are connecting. Review its
    identity and requested access before approving.
  </p>
  <form
    class="flex flex-wrap items-end gap-3"
    onsubmit={(e) => {
      e.preventDefault();
      void inspect();
    }}
  >
    <label class="grid w-full gap-2 sm:w-auto"
      >Device code<input
        id="application-device-code"
        aria-label="Device code"
        class="input input-sm w-full font-mono uppercase"
        maxlength="16"
        required
        bind:value={code}
        oninput={() => (request = undefined)}
        autocomplete="off"
      /></label
    ><button class="btn btn-sm" disabled={busy}>Review request</button>
  </form>
  {#if request}<div class="space-y-3 break-words">
      <h3 class="font-semibold">{request.clientName}</h3>
      <p class="break-all text-sm">Client: {request.clientId}</p>
      <p>Project: {request.scope.projectId}</p>
      <p>Actions: {request.scope.actions.join(", ")}</p>
      <p>Templates: {request.scope.templateIds.join(", ") || "None"}</p>
      <p>Instances: {request.scope.instanceIds.join(", ") || "None"}</p>
      <p>Targets: {request.scope.targetIds.join(", ") || "None"}</p>
      <p class="text-sm">
        Request expires {new Date(request.expiresAt).toLocaleString()}. Approved
        access lasts at most 15 minutes and remains limited by your current
        permissions.
      </p>
      {#if request.state === "pending"}<div class="flex gap-3">
          <button
            class="btn btn-sm btn-primary"
            disabled={busy}
            onclick={() => void approve(true)}>Approve access</button
          ><button
            class="btn btn-sm"
            disabled={busy}
            onclick={() => void approve(false)}>Deny</button
          >
        </div>{:else}<p>This request is {request.state}.</p>{/if}
    </div>{/if}
  {#if error}<p role="alert" class="text-error">{error}</p>{/if}{#if message}<p
      role="status"
    >
      {message}
    </p>{/if}
</section>
