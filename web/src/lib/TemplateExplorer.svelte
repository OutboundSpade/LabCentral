<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { ArrowLeft, ArrowRight, BookOpen, Monitor, Search, Server, RotateCw, Users } from "@lucide/svelte";
  import { api, APIError, session, refresh, date } from "$lib/api";
  import { sessionScope, learnPath, kindLabel } from "$lib/learning";
  import { render, prose } from "$lib/markdown";
  import Notice from "$lib/Notice.svelte";
  import type { PublicTemplate, LearningLab } from "$lib/types";
  let { labs = null, practiceError = "", retryPractice }: { labs?: LearningLab[] | null; practiceError?: string; retryPractice?: () => Promise<void> } = $props();
  let mounted = $state(false);
  onMount(() => { mounted = true; });
  const id = $derived(browser && mounted ? page.url.searchParams.get("template") ?? "" : "");
  const query = $derived(browser && mounted ? page.url.searchParams.get("q") ?? "" : "");
  let templates = $state<PublicTemplate[] | null>(null);
  let template = $state<PublicTemplate | null>(null);
  let failure = $state("");
  let detailFailure = $state("");
  let selected = $state("");
  let scope = "";
  let seq = 0;
  let detailSeq = 0;
  let exploredID = "";
  let exploredScope = "";
  async function load() {
    const s = scope, n = ++seq;
    if (!s) return;
    try {
      const next = await api<{templates: PublicTemplate[]}>("/api/templates");
      if (s !== scope || n !== seq) return;
      templates = next.templates;
      failure = "";
    } catch (e) {
      if (s !== scope || n !== seq) return;
      if (e instanceof APIError && [401,403].includes(e.status)) { templates = null; template = null; void refresh(); }
      failure = (e as Error).message;
    }
  }
  async function detail(target: string) {
    const s = scope, n = ++detailSeq;
    if (!s || !target) return;
    try {
      const next = await api<PublicTemplate>(`/api/templates/${encodeURIComponent(target)}`);
      if (s !== scope || n !== detailSeq) return;
      template = next;
      detailFailure = "";
    } catch (e) {
      if (s !== scope || n !== detailSeq) return;
      if (e instanceof APIError && [401,403].includes(e.status)) { template = null; templates = null; void refresh(); }
      detailFailure = (e as Error).message;
    }
  }
  $effect(() => {
    const s = sessionScope($session);
    untrack(() => {
      if (s === scope) return;
      scope = s; seq++; detailSeq++;
      templates = null; template = null; failure = ""; detailFailure = ""; selected = "";
      if (s) void load();
    });
  });
  $effect(() => {
    const target = id, s = sessionScope($session);
    untrack(() => {
      if (target === exploredID && s === exploredScope) return;
      exploredID = target; exploredScope = s;
      detailSeq++; template = null; detailFailure = ""; selected = "";
      if (target && s) void detail(target);
    });
  });
  const latest = $derived([...new Map((templates ?? []).toReversed().map(t => [t.name, t])).values()].toSorted((a,b) => a.name.localeCompare(b.name)));
  const revisions = $derived((templates ?? []).filter(t => t.name === template?.name));
  const visible = $derived(latest.filter(t => `${t.name} ${t.description} ${t.commitTitle}`.toLowerCase().includes(query.trim().toLowerCase())));
  const stage = $derived(template?.stages.find(s => s.id === selected) ?? template?.stages[0]);
  const practices = $derived((labs ?? []).filter(l => l.template === template?.id));
  function path(target: string) { const params = new URLSearchParams(); params.set("template", target); if (query) params.set("q", query); return `/labs/?${params}`; }
  function catalogPath(value: string) { const params = new URLSearchParams({view: "templates"}); if (value) params.set("q", value); return `/labs/?${params}`; }
  function search(value: string) { void goto(catalogPath(value), {replaceState:true, noScroll:true, keepFocus:true}); }
</script>

{#if id}
  <a class="btn btn-sm btn-ghost min-h-11 mb-4 -ml-3 text-base-content/70" href={catalogPath(query)}><ArrowLeft size={15} />All templates</a>
  {#if template}
    <div class="grid items-start gap-8 lg:grid-cols-[17rem_minmax(0,1fr)] lg:gap-10">
      <aside class="contents lg:block lg:min-w-0 lg:space-y-6">
        <div class="order-0">
          <h2 class="break-words text-2xl font-semibold tracking-tight">{template.name}</h2>
          {#if template.description}<p class="mt-2 text-sm leading-relaxed text-base-content/70">{template.description}</p>{/if}
          {#if revisions.length > 1}<label class="label mt-4 mb-1.5 text-xs" for="template-revision">Revision</label><select id="template-revision" class="select select-sm min-h-11 w-full" value={template.id} onchange={(e) => goto(path(e.currentTarget.value))}>{#each revisions as revision (revision.id)}<option value={revision.id}>{revision.commit.slice(0,7)} · {revision.commitTitle || "Imported revision"}</option>{/each}</select>
          {:else}<p class="mt-3 text-xs text-base-content/60"><span class="font-mono">{template.commit.slice(0,12)}</span>{#if template.committedAt} · {date(template.committedAt, {dateStyle:"medium"})}{/if}</p>{/if}
        </div>
        {#if $session?.user.admin}<div class="order-2 flex flex-wrap gap-2">
          <a class="btn btn-sm min-h-11 lg:min-h-8" href={`/admin/labs/?template=${encodeURIComponent(template.id)}`}><BookOpen size={15} />Publish practice</a>
          {#if template.imagesBuilt}<a class="btn btn-sm btn-ghost min-h-11 lg:min-h-8" href={`/admin/?template=${encodeURIComponent(template.id)}`}><Users size={15} />Prepare meeting</a>
          {:else}<a class="btn btn-sm btn-ghost min-h-11 lg:min-h-8" href="/admin/templates/"><Server size={15} />Build images</a>{/if}
        </div>{/if}
        <section aria-labelledby="template-environment-title" class="panel order-4 p-4">
          <h3 id="template-environment-title" class="eyebrow">Machines</h3>
          <ul class="mt-2 space-y-2.5">
            {#each template.machines as machine (machine.name)}<li class="flex min-w-0 items-start gap-2.5 text-sm">
              {#if machine.access.includes("desktop")}<Monitor size={15} class="mt-0.5 shrink-0 text-base-content/60" />{:else}<Server size={15} class="mt-0.5 shrink-0 text-base-content/60" />{/if}
              <span class="min-w-0"><span class="block break-words font-medium">{machine.name}</span><span class="block text-xs text-base-content/60">{machine.cpu} CPU · {machine.memory} · {machine.disk}</span></span>
            </li>{/each}
          </ul>
        </section>
      </aside>
      <section class="order-3 min-w-0 lg:order-none" aria-labelledby="template-stages-title">
        {#if template.handover}<details class="collapse collapse-arrow panel mb-6">
          <summary class="collapse-title min-h-11 text-sm font-medium">Environment handover</summary>
          <div class="collapse-content"><div class={prose}>{@html render(template.handover)}</div></div>
        </details>{/if}
        <h3 id="template-stages-title" class="sr-only">Stages</h3>
        {#if template.stages.length}
          <div class="flex flex-wrap items-center gap-3">
            <label class="text-sm text-base-content/70" for="template-preview-stage">Stage</label>
            <select id="template-preview-stage" class="select select-sm min-h-11 min-w-0 flex-1 lg:min-h-9" value={stage?.id} onchange={(e) => selected = e.currentTarget.value}>
              {#each template.stages as s, i (s.id)}<option value={s.id}>{i+1}. {s.title}</option>{/each}
            </select>
          </div>
          {#if stage}<article class="mt-6" aria-label="Stage preview">
            <p class="eyebrow">{kindLabel(stage.kind)}</p>
            <h4 class="mt-1 text-xl font-semibold tracking-tight">{stage.title}</h4>
            <div class={[prose, "mt-4 break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto"]}>{@html render(stage.instructions)}</div>
            {#if stage.prompt}<p class="panel mt-5 p-4 text-sm leading-relaxed">{stage.prompt}</p>{/if}
          </article>{/if}
        {:else}<p class="text-sm text-base-content/70">This revision has no stages.</p>{/if}
        <section class="mt-10 border-t border-base-content/8 pt-5" aria-labelledby="template-practice-title">
          <h4 id="template-practice-title" class="text-sm font-semibold">Practice labs</h4>
          {#if practiceError && labs}<p class="mt-2 text-xs text-warning" role="status">Updates are unavailable. Showing the last loaded labs.</p>{/if}
          {#if !labs && practiceError}<p class="mt-2 text-sm text-warning" role="status">Published practice could not be loaded ({practiceError}).</p>{#if retryPractice}<button class="btn btn-sm min-h-11 mt-3" onclick={retryPractice}><RotateCw size={15} />Retry practice list</button>{/if}
          {:else if !labs}<p class="mt-2 text-sm text-base-content/70" role="status">Loading…</p>
          {:else if practices.length}<ul class="mt-2 space-y-1">{#each practices as lab (lab.id)}<li><a class="link link-hover inline-flex min-h-9 items-center gap-2 text-sm" href={learnPath(lab.id)}>{lab.title}<ArrowRight size={14} /></a><span class="ml-2 text-xs text-base-content/60">{lab.stepCount} stages · {lab.estimatedMinutes} min</span></li>{/each}</ul>
          {:else}<p class="mt-2 text-sm text-base-content/70">No practice lab is published from this revision yet.</p>{/if}
        </section>
      </section>
    </div>
  {:else if detailFailure}<Notice message={detailFailure} /><button class="btn btn-sm min-h-11" onclick={() => detail(id)}><RotateCw size={15} />Retry template</button>
  {:else}<div role="status" class="space-y-4"><div class="skeleton h-7 w-64 max-w-full"></div><div class="skeleton h-40 w-full"></div><span class="sr-only">Loading template…</span></div>{/if}
{:else}
  <section aria-label="Template catalog">
    <label class="input input-sm min-h-11 w-full sm:max-w-xs lg:min-h-9"><Search size={15} aria-hidden="true" class="opacity-60" /><input id="template-search" type="search" aria-label="Search templates" value={query} oninput={(e) => search(e.currentTarget.value)} placeholder="Search templates" autocomplete="off" /></label>
    {#if failure && templates}<p class="mt-4 text-sm text-warning" role="status">Updates are unavailable ({failure}). Showing the last loaded catalog.</p>{/if}
    {#if templates}
      {#if query}<p class="sr-only" role="status">{visible.length} of {latest.length} templates shown</p>{/if}
      <ul class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {#each visible as t (t.id)}<li class="panel flex min-w-0 flex-col p-5 transition-colors hover:border-base-content/20">
          <h2 class="break-words text-base font-semibold leading-snug"><a class="hover:underline" href={path(t.id)}>{t.name}</a></h2>
          <p class="mt-1.5 line-clamp-2 text-sm leading-relaxed text-base-content/70">{t.description || "A pinned lab environment."}</p>
          <div class="mt-auto flex items-center gap-3 pt-5">
            <p class="flex-1 text-xs text-base-content/60">{t.machines.length} {t.machines.length === 1 ? "machine" : "machines"} · {t.stageCount} {t.stageCount === 1 ? "stage" : "stages"}</p>
            <a class="btn btn-sm min-h-11 lg:min-h-8" href={path(t.id)} aria-label={`Explore ${t.name} revision ${t.commit.slice(0,7)}`}>Explore<ArrowRight size={15} /></a>
          </div>
        </li>
        {:else}<li class="py-10 text-center sm:col-span-2 xl:col-span-3"><BookOpen size={22} class="mx-auto text-base-content/60" /><h2 class="mt-3 font-semibold">{templates.length ? "No matching templates" : "No templates yet"}</h2>{#if query}<button class="btn btn-sm btn-ghost min-h-11 mt-3" onclick={() => search("")}>Clear search</button>{/if}</li>{/each}
      </ul>
    {:else if failure}<Notice message={failure} /><button class="btn btn-sm min-h-11" onclick={load}><RotateCw size={15} />Retry catalog</button>
    {:else}<div class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3" role="status">{#each [1,2,3] as n (n)}<div class="skeleton h-36 w-full rounded-box"></div>{/each}<span class="sr-only">Loading templates…</span></div>{/if}
  </section>
{/if}
