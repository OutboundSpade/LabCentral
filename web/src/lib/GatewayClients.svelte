<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    KeyRound,
    Plus,
    RefreshCw,
    Copy,
    Check,
    Power,
    CircleAlert,
  } from "@lucide/svelte";
  import { api, mutationHeaders } from "./api";
  import Modal from "./Modal.svelte";
  type Client = { id: string; name: string; disabled: boolean };
  let { enabled = true }: { enabled?: boolean } = $props();
  let clients = $state<Client[]>([]),
    loaded = $state(false),
    loading = $state(false);
  let name = $state(""),
    error = $state(""),
    formError = $state(""),
    busy = $state(false);
  let registering = $state(false),
    confirming = $state(false),
    selected = $state<Client>();
  let created = $state<{ client: Client; clientSecret: string }>(),
    reveal = $state(false),
    copied = $state("");
  const abort = new AbortController();
  onMount(() => {
    if (enabled) void refresh();
  });
  onDestroy(() => {
    abort.abort();
    created = undefined;
  });
  async function refresh() {
    if (loading) return;
    loading = true;
    error = "";
    try {
      const result = await api<{ items: Client[] }>("/admin/gateway-clients", {
        signal: abort.signal,
        fresh: true,
      });
      if (!abort.signal.aborted) {
        clients = result.items ?? [];
        loaded = true;
      }
    } catch (e) {
      if (!abort.signal.aborted)
        error = e instanceof Error ? e.message : "Could not load API clients.";
    } finally {
      loading = false;
    }
  }
  async function create() {
    if (busy || !name.trim()) return;
    busy = true;
    formError = "";
    try {
      const result = await api<{ client: Client; clientSecret: string }>(
        "/admin/gateway-clients",
        {
          method: "POST",
          signal: abort.signal,
          headers: mutationHeaders(),
          body: JSON.stringify({ name: name.trim() }),
        },
      );
      if (abort.signal.aborted) return;
      created = result;
      reveal = false;
      copied = "";
      registering = false;
      name = "";
      clients = [...clients, result.client];
      loaded = true;
      await refresh();
    } catch (e) {
      if (!abort.signal.aborted)
        formError = e instanceof Error ? e.message : "Registration failed.";
    } finally {
      busy = false;
    }
  }
  async function changeStatus(client: Client) {
    if (busy) return;
    busy = true;
    error = "";
    formError = "";
    try {
      await api(
        `/admin/gateway-clients/${encodeURIComponent(client.id)}/status`,
        {
          method: "POST",
          signal: abort.signal,
          headers: mutationHeaders(),
          body: JSON.stringify({ disabled: !client.disabled }),
        },
      );
      if (abort.signal.aborted) return;
      confirming = false;
      clients = clients.map((c) =>
        c.id === client.id ? { ...c, disabled: !client.disabled } : c,
      );
      await refresh();
    } catch (e) {
      if (!abort.signal.aborted) {
        const message =
          e instanceof Error ? e.message : "Could not update API client.";
        if (confirming) formError = message;
        else error = message;
      }
    } finally {
      busy = false;
    }
  }
  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      copied = label;
    } catch {
      error = "Clipboard unavailable. Select and copy the field manually.";
    }
  }
</script>

<section aria-labelledby="api-clients-title" class="space-y-5">
  <header class="flex flex-wrap items-start justify-between gap-4">
    <div class="max-w-2xl">
      <h2 id="api-clients-title" class="text-lg font-semibold">API clients</h2>
      <p class="mt-2 text-sm text-base-content/70">
        External applications, such as an SSH gateway, can request temporary
        access on a user's behalf. Registration identifies the application; each
        user still approves its requested access.
      </p>
    </div>
    {#if enabled}<div class="flex flex-wrap gap-2">
        <button
          type="button"
          class="btn btn-sm"
          disabled={loading || busy}
          onclick={() => void refresh()}
          ><RefreshCw size={16} />Refresh clients</button
        ><button
          type="button"
          class="btn btn-sm btn-primary"
          disabled={busy || !!created}
          onclick={() => {
            name = "";
            formError = "";
            registering = true;
          }}><Plus size={16} />Register API client</button
        >
      </div>{/if}
  </header>
  {#if !enabled}<p class="rounded-box border border-base-300 p-5 text-sm">
      API clients require authenticated mode. Configure local or OIDC sign-in
      before registering an application.
    </p>
  {:else}
    {#if error}<div role="alert" class="alert alert-error alert-soft">
        <CircleAlert size={20} /><span class="min-w-0 break-words">{error}</span
        ><button
          type="button"
          class="btn btn-sm"
          disabled={loading}
          onclick={() => void refresh()}>Retry</button
        >
      </div>{/if}
    {#if created}<section
        aria-labelledby="client-credentials-title"
        class="min-w-0 rounded-box border border-base-300 bg-base-200 p-5"
      >
        <h3
          id="client-credentials-title"
          class="flex items-center gap-2 font-semibold"
        >
          <KeyRound size={18} />Save credentials for {created.client.name}
        </h3>
        <p class="mt-2 text-sm text-base-content/70">
          The client secret is shown only now. Save both values in the
          application's protected configuration before dismissing this panel or
          leaving the page.
        </p>
        <div class="mt-4 grid gap-4">
          <div>
            <label class="mb-2 block text-sm" for="new-client-id"
              >Client ID</label
            >
            <div class="flex min-w-0 flex-wrap gap-2">
              <input
                id="new-client-id"
                class="input input-sm min-w-0 grow font-mono"
                readonly
                value={created.client.id}
              /><button
                type="button"
                class="btn btn-sm"
                aria-label="Copy client ID"
                onclick={() => void copy(created!.client.id, "ID")}
                >{#if copied === "ID"}<Check size={16} />{:else}<Copy
                    size={16}
                  />{/if}Copy ID</button
              >
            </div>
          </div>
          <div>
            <label class="mb-2 block text-sm" for="new-client-secret"
              >Client secret</label
            >
            <div class="flex min-w-0 flex-wrap gap-2">
              <input
                id="new-client-secret"
                class="input input-sm min-w-0 grow font-mono"
                type={reveal ? "text" : "password"}
                readonly
                value={created.clientSecret}
                autocomplete="off"
              /><button
                type="button"
                class="btn btn-sm"
                onclick={() => (reveal = !reveal)}
                >{reveal ? "Hide secret" : "Show secret"}</button
              ><button
                type="button"
                class="btn btn-sm"
                aria-label="Copy client secret"
                onclick={() => void copy(created!.clientSecret, "secret")}
                >{#if copied === "secret"}<Check size={16} />{:else}<Copy
                    size={16}
                  />{/if}Copy secret</button
              >
            </div>
          </div>
        </div>
        {#if copied}<p role="status" class="mt-3 text-sm">
            Client {copied} copied.
          </p>{/if}
        <button
          type="button"
          class="btn btn-sm mt-4"
          onclick={() => {
            created = undefined;
            reveal = false;
            copied = "";
          }}>Dismiss credentials</button
        >
      </section>{/if}
    {#if !loaded && loading}<p
        role="status"
        class="py-6 text-sm text-base-content/70"
      >
        Loading API clients…
      </p>
    {:else if loaded && !clients.length && !error}<div
        class="rounded-box border border-base-300 p-6"
      >
        <KeyRound size={24} class="mb-3 text-base-content/60" />
        <h3 class="font-medium">No API clients registered</h3>
        <p class="mt-2 text-sm text-base-content/70">
          Register an application when you are ready to connect it. Installed
          plugins are managed in the Plugins tab.
        </p>
      </div>
    {:else if clients.length}<ul
        class="divide-y divide-base-300 rounded-box border border-base-300"
      >
        {#each clients as client (client.id)}<li
            class="flex min-w-0 flex-wrap items-center justify-between gap-4 p-5"
          >
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h3 class="break-words font-medium">{client.name}</h3>
                <span class="badge badge-sm"
                  >{client.disabled ? "Disabled" : "Enabled"}</span
                >
              </div>
              <p class="mt-2 break-all font-mono text-xs text-base-content/70">
                {client.id}
              </p>
            </div>
            <button
              type="button"
              class="btn btn-sm"
              disabled={busy}
              aria-label={(client.disabled ? "Enable " : "Disable ") +
                client.name}
              onclick={() => {
                if (client.disabled) void changeStatus(client);
                else {
                  selected = client;
                  formError = "";
                  confirming = true;
                }
              }}
              ><Power size={16} />{client.disabled
                ? "Enable"
                : "Disable"}</button
            >
          </li>{/each}
      </ul>{/if}
  {/if}
</section>

<Modal bind:open={registering} title="Register API client" closeDisabled={busy}>
  <form
    class="space-y-4"
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <p class="text-sm text-base-content/70">
      Choose a name users will recognize when reviewing an access request.
    </p>
    <fieldset class="fieldset" disabled={busy}>
      <label class="label" for="api-client-name">Application name</label><input
        id="api-client-name"
        class="input input-sm w-full"
        bind:value={name}
        required
        maxlength="128"
        placeholder="Classroom SSH gateway"
      />
    </fieldset>
    {#if formError}<p role="alert" class="text-sm text-error">
        {formError}
      </p>{/if}
    <div class="mt-6 flex flex-wrap justify-end gap-2">
      <button
        type="button"
        class="btn btn-sm"
        disabled={busy}
        onclick={() => (registering = false)}>Cancel</button
      ><button
        type="submit"
        class="btn btn-sm btn-primary"
        disabled={busy || !name.trim()}
        >{busy ? "Registering…" : "Register client"}</button
      >
    </div>
  </form>
</Modal>
<Modal bind:open={confirming} title="Disable API client?" closeDisabled={busy}>
  <p class="break-words">
    Disable <strong>{selected?.name}</strong>? Requests from this application
    will be blocked. Existing access sessions close when their authorization is
    rechecked.
  </p>
  {#if formError}<p role="alert" class="mt-3 text-sm text-error">
      {formError}
    </p>{/if}
  <div class="mt-6 flex flex-wrap justify-end gap-2">
    <button
      type="button"
      class="btn btn-sm"
      disabled={busy}
      onclick={() => (confirming = false)}>Cancel</button
    ><button
      type="button"
      class="btn btn-sm btn-error"
      disabled={busy || !selected}
      onclick={() => selected && void changeStatus(selected)}
      >Disable client</button
    >
  </div>
</Modal>
