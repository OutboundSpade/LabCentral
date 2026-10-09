<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { ArrowLeft, ArrowRight, BookOpen, GitCommitHorizontal, ListChecks, Monitor, Search, Server, RotateCw, Users } from "@lucide/svelte";
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
  function search(value: string) { const params = new URLSearchParams(); if (value) params.set("q", value); void goto(`/labs/${params.size ? "?" + params : ""}`, {replaceState:true, noScroll:true, keepFocus:true}); }
</script>

{#if id}
  <a class="btn btn-sm btn-ghost min-h-11 mb-5 -ml-3" href={query ? `/labs/?q=${encodeURIComponent(query)}` : "/labs/"}><ArrowLeft size={15} />All templates</a>
  {#if template}
    <div class="grid items-start gap-8 lg:grid-cols-[19rem_minmax(0,1fr)] lg:gap-12">
      <aside class="contents lg:block lg:min-w-0 lg:space-y-6">
        <div class="order-0">
          <h2 class="break-words text-2xl font-semibold tracking-tight">{template.name}</h2>
          {#if template.description}<p class="mt-3 text-sm leading-relaxed text-base-content/75">{template.description}</p>{/if}
        </div>
        <section aria-labelledby="template-source-title" class="order-1 border-t border-base-content/10 pt-5">
          <h3 id="template-source-title" class="flex items-center gap-2 text-sm font-semibold"><GitCommitHorizontal size={16} />Pinned revision</h3>
          {#if revisions.length > 1}<label class="label mt-3 mb-2 text-xs" for="template-revision">Revision</label><select id="template-revision" class="select select-sm min-h-11 w-full" value={template.id} onchange={(e) => goto(path(e.currentTarget.value))}>{#each revisions as revision (revision.id)}<option value={revision.id}>{revision.commit.slice(0,7)} · {revision.commitTitle || "Imported revision"}</option>{/each}</select>{/if}
          <p class="mt-2 font-mono text-xs lg:block" class:hidden={revisions.length > 1}>{template.commit.slice(0,12)}</p>
          {#if template.commitTitle}<p class="mt-2 text-sm text-base-content/75 lg:block" class:hidden={revisions.length > 1}>{template.commitTitle}</p>{/if}
          {#if template.committedAt}<p class="mt-1 text-xs text-base-content/75">{date(template.committedAt, {dateStyle:"medium"})}</p>{/if}
          <p class="mt-3 text-xs text-base-content/75">{template.imagesBuilt ? "Images built" : "Images not built"} · {template.stageCount} authored stages</p>
        </section>
        <section aria-labelledby="template-environment-title" class="order-4 border-t border-base-content/10 pt-5">
          <h3 id="template-environment-title" class="flex items-center gap-2 text-sm font-semibold"><Server size={16} />Environment</h3>
          <ul class="mt-3 divide-y divide-base-content/10">
            {#each template.machines as machine (machine.name)}<li class="py-3 first:pt-0">
              <p class="flex min-w-0 items-start gap-2 text-sm font-medium">{#if machine.access.includes("desktop")}<Monitor size={15} class="mt-0.5 shrink-0" />{:else}<Server size={15} class="mt-0.5 shrink-0" />{/if}<span class="min-w-0 break-words">{machine.name}</span></p>
              <p class="mt-1 text-xs leading-relaxed text-base-content/75">{machine.cpu} CPUs · {machine.memory} memory · {machine.disk} disk</p>
              <p class="mt-1 text-xs text-base-content/75">{machine.access.join(" · ") || "No interactive access"}</p>
            </li>{/each}
          </ul>
        </section>
        {#if $session?.user.admin}<div class="order-2 flex flex-wrap gap-2 border-t border-base-content/10 pt-5">
          <a class="btn btn-sm min-h-11" href={`/admin/labs/?template=${encodeURIComponent(template.id)}`}><BookOpen size={15} />Publish practice</a>
          {#if template.imagesBuilt}<a class="btn btn-sm btn-ghost min-h-11" href={`/admin/?template=${encodeURIComponent(template.id)}`}><Users size={15} />Prepare meeting</a>
          {:else}<a class="btn btn-sm btn-ghost min-h-11" href="/admin/templates/"><Server size={15} />Build images</a><p class="w-full text-xs leading-relaxed text-base-content/75">Build this pinned revision’s images in template administration before preparing a meeting.</p>{/if}
        </div>{/if}
      </aside>
      <section class="order-3 min-w-0 lg:order-none lg:border-l lg:border-base-content/10 lg:pl-8" aria-labelledby="template-stages-title">
        {#if template.handover}<details class="collapse collapse-arrow border border-base-content/10 mb-6">
          <summary class="collapse-title text-sm font-medium">Environment handover</summary>
          <div class="collapse-content"><div class={prose}>{@html render(template.handover)}</div></div>
        </details>{/if}
        <div class="flex flex-wrap items-baseline justify-between gap-2"><h3 id="template-stages-title" class="text-base font-semibold">Explore stages</h3><span class="text-xs text-base-content/75">Preview · no workspace started</span></div>
        <p class="mt-2 text-sm text-base-content/75">Choose any stage to read its student instructions.</p>
        {#if template.stages.length}
          <label class="label mt-5 mb-2 text-sm" for="template-preview-stage">Stage</label>
          <select id="template-preview-stage" class="select select-sm min-h-11 w-full" value={stage?.id} onchange={(e) => selected = e.currentTarget.value}>
            {#each template.stages as s, i (s.id)}<option value={s.id}>{i+1}. {s.title}</option>{/each}
          </select>
          {#if stage}<article class="mt-6 border-t border-base-content/10 pt-6" aria-label="Stage preview">
            <p class="text-xs text-base-content/75">{kindLabel(stage.kind)}</p>
            <h4 class="mt-2 text-xl font-semibold tracking-tight">{stage.title}</h4>
            <div class={[prose, "mt-5 break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto"]}>{@html render(stage.instructions)}</div>
            {#if stage.prompt}<p class="mt-5 border-t border-base-content/10 pt-4 text-sm leading-relaxed">{stage.prompt}</p>{/if}
          </article>{/if}
        {:else}<p class="mt-5 text-sm text-base-content/75">This revision has no authored stages. Its environment can still be used for a meeting.</p>{/if}
        <section class="mt-8 border-t border-base-content/10 pt-5" aria-labelledby="template-practice-title">
          <h4 id="template-practice-title" class="text-sm font-semibold">Practice this revision</h4>
          {#if practiceError && labs}<p class="mt-2 text-xs text-warning" role="status">Practice updates are unavailable. Showing the last loaded publications.</p>{/if}
          {#if !labs && practiceError}<p class="mt-2 text-sm text-warning" role="status">Published practice could not be loaded ({practiceError}).</p>{#if retryPractice}<button class="btn btn-sm min-h-11 mt-3" onclick={retryPractice}><RotateCw size={15} />Retry practice list</button>{/if}
          {:else if !labs}<p class="mt-2 text-sm text-base-content/75" role="status">Loading published practice…</p>
          {:else if practices.length}<ul class="mt-2 divide-y divide-base-content/10">{#each practices as lab (lab.id)}<li class="py-3"><a class="link inline-flex items-center gap-2 text-sm" href={learnPath(lab.id)}>{lab.title}<ArrowRight size={14} /></a><p class="mt-1 text-xs text-base-content/75">{lab.stepCount} stages · About {lab.estimatedMinutes} min</p></li>{/each}</ul>
          {:else}<p class="mt-2 text-sm text-base-content/75">No practice lab is published from this revision yet. Meeting exercises are chosen by your instructor.</p>{/if}
        </section>
      </section>
    </div>
  {:else if detailFailure}<Notice message={detailFailure} /><button class="btn btn-sm min-h-11" onclick={() => detail(id)}><RotateCw size={15} />Retry template</button>
  {:else}<div role="status" class="space-y-4"><div class="skeleton h-7 w-64 max-w-full"></div><div class="skeleton h-40 w-full"></div><span class="sr-only">Loading template…</span></div>{/if}
{:else}
  <section aria-label="Template catalog">
    <label class="label mb-2 text-sm" for="template-search">Search templates</label>
    <label class="input input-sm min-h-11 w-full sm:max-w-sm"><Search size={16} aria-hidden="true" /><input id="template-search" type="search" value={query} oninput={(e) => search(e.currentTarget.value)} placeholder="Environment or topic" autocomplete="off" /></label>
    {#if failure && templates}<p class="mt-4 text-sm text-warning" role="status">Template updates are unavailable ({failure}). Showing the last loaded catalog.</p>{/if}
    {#if templates}
      <p class="mt-5 text-xs text-base-content/75" role="status">{query ? `${visible.length} of ${latest.length} templates shown` : `${latest.length} ${latest.length === 1 ? "template" : "templates"} · ${templates.length} pinned ${templates.length === 1 ? "revision" : "revisions"}`}</p>
      <ul class="mt-3 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {#each visible as t (t.id)}<li class="card card-border min-w-0 border-base-content/10 bg-base-100"><div class="card-body gap-0 p-5">
          <div class="flex flex-wrap items-center gap-2 text-xs text-base-content/75"><GitCommitHorizontal size={14} /><code>{t.commit.slice(0,7)}</code><span class="ml-auto">{t.imagesBuilt ? "Images built" : "Not built"}</span></div>
          <h2 class="card-title mt-4 break-words text-lg leading-snug"><a class="link link-hover" href={path(t.id)}>{t.name}</a></h2>
          <p class="mt-2 text-sm leading-relaxed text-base-content/75">{t.description || "Explore this pinned lab environment and its student instructions."}</p>
          <div class="mt-5 flex flex-wrap gap-x-4 gap-y-2 text-xs text-base-content/75"><span class="inline-flex items-center gap-1.5"><Server size={14} />{t.machines.length} machines</span><span class="inline-flex items-center gap-1.5"><ListChecks size={14} />{t.stageCount} stages</span><span>{(templates ?? []).filter(r => r.name === t.name).length} revisions</span></div>
          <p class="mt-2 break-words text-xs leading-relaxed text-base-content/75">{t.machines.map(m=>m.name).join(" · ")}</p>
          <div class="card-actions mt-5 border-t border-base-content/10 pt-4"><a class="btn btn-sm min-h-11 w-full" href={path(t.id)} aria-label={`Explore ${t.name} revision ${t.commit.slice(0,7)}`}>Explore template<ArrowRight size={15} /></a></div>
        </div></li>
        {:else}<li class="py-6 sm:col-span-2 xl:col-span-3"><BookOpen size={22} class="text-base-content/75" /><h2 class="mt-3 font-semibold">{templates.length ? "No matching templates" : "No templates imported yet"}</h2><p class="mt-2 text-sm text-base-content/75">{templates.length ? "Try another environment or topic." : "Templates appear here after an administrator imports their repository."}</p>{#if query}<button class="btn btn-sm btn-ghost min-h-11 mt-3" onclick={() => search("")}>Clear search</button>{/if}</li>{/each}
      </ul>
    {:else if failure}<Notice message={failure} /><button class="btn btn-sm min-h-11" onclick={load}><RotateCw size={15} />Retry catalog</button>
    {:else}<div class="mt-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-3" role="status">{#each [1,2,3] as n (n)}<div class="skeleton h-60 w-full"></div>{/each}<span class="sr-only">Loading templates…</span></div>{/if}
  </section>
{/if}
