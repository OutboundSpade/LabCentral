<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import {
    ArrowLeft,
    ArrowRight,
    BookOpen,
    Clock,
    Dices,
    Gauge,
    ListChecks,
    Monitor,
    Play,
    RotateCw,
    Search,
    Server,
    Shuffle,
    Users,
  } from "@lucide/svelte";
  import { api, APIError, session, refresh, date } from "$lib/api";
  import { sessionScope, learnPath, kindLabel } from "$lib/learning";
  import {
    attempts as ownAttempts,
    legacy as legacyLabs,
    attemptBlocked,
    baselineBlocked,
    baselineHint,
    scenarioBlocked,
    desktopWorkstation,
    scenarios as scenarioList,
    difficultyLabel,
    minutes,
    breakRange,
    type AttemptCatalog,
    type LibraryTemplate,
  } from "$lib/library";
  import { render, prose } from "$lib/markdown";
  import Notice from "$lib/Notice.svelte";
  import AttemptButton from "$lib/AttemptButton.svelte";
  let {
    catalog = null,
    catalogError = "",
    retryCatalog,
  }: {
    catalog?: AttemptCatalog | null;
    catalogError?: string;
    retryCatalog?: () => Promise<void>;
  } = $props();
  let mounted = $state(false);
  onMount(() => {
    mounted = true;
  });
  const param = (name: string) =>
    browser && mounted ? (page.url.searchParams.get(name) ?? "") : "";
  const id = $derived(param("template"));
  const query = $derived(param("q"));
  const category = $derived(param("category"));
  const difficulty = $derived(param("difficulty"));
  const mode = $derived(param("mode"));
  let templates = $state<LibraryTemplate[] | null>(null);
  let template = $state<LibraryTemplate | null>(null);
  let failure = $state("");
  let detailFailure = $state("");
  let selected = $state("");
  let scope = "";
  let seq = 0;
  let detailSeq = 0;
  let exploredID = "";
  let exploredScope = "";
  const audienceScope = $derived(
    $session ? `${sessionScope($session)}:${$session.user.admin}` : "",
  );
  async function load() {
    const s = scope,
      n = ++seq;
    if (!s) return;
    try {
      const next = await api<{ templates: LibraryTemplate[] }>("/api/templates");
      if (s !== scope || n !== seq) return;
      templates = next.templates;
      failure = "";
    } catch (e) {
      if (s !== scope || n !== seq) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        templates = null;
        template = null;
        void refresh();
      }
      failure = (e as Error).message;
    }
  }
  async function detail(target: string) {
    const s = scope,
      n = ++detailSeq;
    if (!s || !target) return;
    try {
      const next = await api<LibraryTemplate>(
        `/api/templates/${encodeURIComponent(target)}`,
      );
      if (s !== scope || n !== detailSeq || target !== exploredID) return;
      // The detail view stays on the exact requested revision.
      if (next.id !== target) throw new Error("This template revision is unavailable.");
      template = next;
      detailFailure = "";
    } catch (e) {
      if (s !== scope || n !== detailSeq || target !== exploredID) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        template = null;
        templates = null;
        void refresh();
      }
      detailFailure = (e as Error).message;
    }
  }
  $effect(() => {
    const s = audienceScope;
    untrack(() => {
      if (s === scope) return;
      scope = s;
      seq++;
      detailSeq++;
      templates = null;
      template = null;
      failure = "";
      detailFailure = "";
      selected = "";
      if (s) void load();
    });
  });
  $effect(() => {
    const target = id,
      s = audienceScope;
    untrack(() => {
      if (target === exploredID && s === exploredScope) return;
      exploredID = target;
      exploredScope = s;
      detailSeq++;
      template = null;
      detailFailure = "";
      selected = "";
      if (target && s) void detail(target);
    });
  });
  const practicable = (t: LibraryTemplate) => !baselineBlocked(t);
  const randomized = (t: LibraryTemplate) =>
    !t.coachOnly && scenarioList(t).length > 0;
  // One card per template name, showing its newest revision.
  const latest = $derived(
    [
      ...new Map(
        (templates ?? []).toReversed().map((t) => [t.name, t]),
      ).values(),
    ].toSorted((a, b) => a.name.localeCompare(b.name)),
  );
  const distinct = (values: string[]) =>
    [...new Set(values.filter(Boolean))].toSorted((a, b) => a.localeCompare(b));
  const categories = $derived(distinct(latest.map((t) => t.library?.category ?? "")));
  const difficulties = $derived(
    distinct(latest.map((t) => t.library?.difficulty ?? "")),
  );
  const anyScenarios = $derived(latest.some(randomized));
  const haystack = (t: LibraryTemplate) =>
    [
      t.name,
      t.description,
      t.commitTitle,
      t.library?.category,
      t.library?.difficulty,
      ...(t.library?.tags ?? []),
      ...scenarioList(t).map((s) => `${s.title} ${s.description}`),
    ]
      .join(" ")
      .toLowerCase();
  const visible = $derived(
    latest.filter(
      (t) =>
        haystack(t).includes(query.trim().toLowerCase()) &&
        (!category || t.library?.category === category) &&
        (!difficulty || t.library?.difficulty === difficulty) &&
        (mode !== "guided" || practicable(t)) &&
        (mode !== "scenarios" || randomized(t)),
    ),
  );
  const filtered = $derived(!!(query || category || difficulty || mode));
  const revisions = $derived(
    (templates ?? []).filter((t) => t.name === template?.name),
  );
  const stage = $derived(
    template?.stages.find((s) => s.id === selected) ?? template?.stages[0],
  );
  const mine = $derived(
    ownAttempts(catalog).filter((lab) => lab.template === template?.id),
  );
  const earlier = $derived(
    legacyLabs(catalog).filter((lab) => lab.template === template?.id),
  );
  function listParams() {
    const params = new URLSearchParams();
    for (const [name, value] of [
      ["q", query],
      ["category", category],
      ["difficulty", difficulty],
      ["mode", mode],
    ])
      if (value) params.set(name, value);
    return params;
  }
  function path(target: string) {
    const params = listParams();
    params.set("template", target);
    return `/labs/?${params}`;
  }
  function listPath(params = listParams()) {
    const text = params.toString();
    return text ? `/labs/?${text}` : "/labs/";
  }
  function set(name: string, value: string) {
    const params = listParams();
    if (value) params.set(name, value);
    else params.delete(name);
    void goto(listPath(params), {
      replaceState: true,
      noScroll: true,
      keepFocus: true,
    });
  }
  function clear() {
    void goto("/labs/", { replaceState: true, noScroll: true, keepFocus: true });
  }
  // Native select text is not clipped by padding, so long titles are shortened.
  function revisionLabel(revision: LibraryTemplate) {
    const title = revision.commitTitle || "Imported revision";
    return `${revision.commit.slice(0, 7)} · ${title.length > 26 ? `${title.slice(0, 25).trimEnd()}…` : title}`;
  }
  const scenarioTitle = (scenarioID?: string) =>
    scenarioList(template).find((s) => s.id === scenarioID)?.title ??
    "Randomized scenario";
</script>

{#if id}
  <a
    class="btn btn-sm btn-ghost min-h-11 mb-4 -ml-3 text-base-content/70"
    href={listPath()}><ArrowLeft size={15} />All templates</a
  >
  {#if template}
    {@const meta = template.library}
    {@const baseline = baselineBlocked(template)}
    {@const scenarioReason = scenarioBlocked(template)}
    {@const busyRun = attemptBlocked(catalog?.run)}
    <div class="grid items-start gap-8 lg:grid-cols-[17rem_minmax(0,1fr)] lg:gap-10">
      <aside class="contents lg:block lg:min-w-0 lg:space-y-6">
        <div class="order-0">
          {#if meta?.category}<p class="mb-1 text-xs font-medium text-base-content/70">
              {meta.category}
            </p>{/if}
          <h2 class="break-words text-2xl font-semibold tracking-tight">
            {template.name}
          </h2>
          {#if template.description}<p
              class="mt-2 text-sm leading-relaxed text-base-content/70"
            >
              {template.description}
            </p>{/if}
          <div class="mt-3 flex flex-wrap items-center gap-1.5">
            {#if template.coachOnly}<span class="badge badge-sm badge-ghost">Coach-only</span>{/if}
            {#if meta?.difficulty}<span class="badge badge-sm badge-soft"
                ><Gauge size={12} aria-hidden="true" />{difficultyLabel(meta.difficulty)}</span
              >{/if}
            {#if meta?.estimatedMinutes}<span class="badge badge-sm badge-soft"
                ><Clock size={12} aria-hidden="true" />{minutes(meta.estimatedMinutes)}</span
              >{/if}
            {#each meta?.tags ?? [] as tag (tag)}<span class="badge badge-sm badge-ghost">{tag}</span>{/each}
          </div>
          {#if revisions.length > 1}<label
              class="label mt-4 mb-1.5 text-xs"
              for="template-revision">Revision</label
            ><select
              id="template-revision"
              class="select select-sm min-h-11 w-full"
              value={template.id}
              onchange={(e) => goto(path(e.currentTarget.value))}
              >{#each revisions as revision (revision.id)}<option value={revision.id}
                  >{revisionLabel(revision)}</option
                >{/each}</select
            ><p class="mt-1.5 break-words text-xs text-base-content/60">
              {template.commitTitle || "Imported revision"}{#if template.committedAt}{` · ${date(template.committedAt, { dateStyle: "medium" })}`}{/if}
            </p>
          {:else}<p class="mt-4 text-xs text-base-content/60">
              <span class="font-mono">{template.commit.slice(0, 12)}</span
              >{#if template.committedAt}
                · {date(template.committedAt, { dateStyle: "medium" })}{/if}
            </p>{/if}
        </div>
        {#if $session?.user.admin}<div class="order-2 flex flex-wrap gap-2">
            {#if template.imagesBuilt}<a
                class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                href={`/admin/?template=${encodeURIComponent(template.id)}`}
                ><Users size={15} />Prepare meeting</a
              >
            {:else}<a
                class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                href="/admin/templates/"><Server size={15} />Build images</a
              >{/if}
          </div>{/if}
        <section
          aria-labelledby="template-environment-title"
          class="rounded-box border border-base-content/9 bg-base-200/70 order-4 p-4"
        >
          <h3 id="template-environment-title" class="text-xs font-medium text-base-content/70">
            Machines
          </h3>
          <ul class="mt-2 space-y-2.5">
            {#each template.machines as machine (machine.name)}<li
                class="flex min-w-0 items-start gap-2.5 text-sm"
              >
                {#if machine.access.includes("desktop")}<Monitor
                    size={15}
                    class="mt-0.5 shrink-0 text-base-content/60"
                    aria-hidden="true"
                  />{:else}<Server
                    size={15}
                    class="mt-0.5 shrink-0 text-base-content/60"
                    aria-hidden="true"
                  />{/if}
                <span class="min-w-0"
                  ><span class="block break-words font-medium">{machine.name}</span><span
                    class="block text-xs text-base-content/60"
                    >{machine.cpu} CPU · {machine.memory} · {machine.disk}</span
                  ></span
                >
              </li>{/each}
          </ul>
        </section>
      </aside>
      <div class="order-3 min-w-0 space-y-10 lg:order-none">
        <section aria-labelledby="template-practice-title">
          <h3 id="template-practice-title" class="text-sm font-semibold">
            Practice this revision
          </h3>
          <p class="mt-1 text-sm text-base-content/70">
            Each start saves a private attempt. Cloud workspaces start separately from
            the lab, using your weekly cloud time.
          </p>
          <ul class="mt-4 grid gap-3 xl:grid-cols-2">
            <li class="card card-sm border border-base-content/9 bg-base-200/70">
              <div class="card-body">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <h4 class="card-title text-base">
                    <ListChecks size={16} aria-hidden="true" />Baseline practice
                  </h4>
                  {#if !baseline}<span class="badge badge-sm badge-ghost"
                      >{template.practiceStageIDs.length}
                      {template.practiceStageIDs.length === 1 ? "stage" : "stages"}</span
                    >{/if}
                </div>
                <p class="text-sm leading-relaxed text-base-content/70">
                  Work through the self-paced stages from the baseline environment.
                </p>
                <p id="baseline-help" class="grow-0 text-xs leading-relaxed text-base-content/70">
                  {baseline || busyRun || baselineHint(template)}
                </p>
                <div class="card-actions mt-1">
                  <AttemptButton
                    template={template.id}
                    label="Start practice"
                    name={`Start practice: ${template.name}`}
                    icon={Play}
                    primary
                    blocked={baseline || busyRun}
                    describedby="baseline-help"
                  />
                </div>
              </div>
            </li>
            {#each scenarioList(template) as scenario (scenario.id)}
              <li
                class="card card-sm border border-base-content/9 bg-base-200/70"
                aria-labelledby={`scenario-${scenario.id}-title`}
              >
                <div class="card-body">
                  <div class="flex flex-wrap items-center justify-between gap-2">
                    <h4 id={`scenario-${scenario.id}-title`} class="card-title text-base">
                      <Shuffle size={16} aria-hidden="true" />{scenario.title}
                    </h4>
                    <span class="badge badge-sm badge-ghost">Randomized</span>
                  </div>
                  {#if scenario.description}<p
                      class="text-sm leading-relaxed text-base-content/70"
                    >
                      {scenario.description}
                    </p>{/if}
                  <p class="flex grow-0 flex-wrap gap-x-3 gap-y-1 text-xs text-base-content/70">
                    {#if scenario.difficulty}<span class="inline-flex items-center gap-1"
                        ><Gauge size={12} aria-hidden="true" />{difficultyLabel(scenario.difficulty)}</span
                      >{/if}
                    {#if scenario.estimatedMinutes}<span class="inline-flex items-center gap-1"
                        ><Clock size={12} aria-hidden="true" />{minutes(scenario.estimatedMinutes)}</span
                      >{/if}
                    <span class="inline-flex items-center gap-1"
                      ><Dices size={12} aria-hidden="true" />{breakRange(scenario)} · {scenario.variantCount}
                      {scenario.variantCount === 1 ? "variant" : "variants"}</span
                    >
                  </p>
                  <p
                    id={`scenario-${scenario.id}-help`}
                    class="grow-0 text-xs leading-relaxed text-base-content/70"
                  >
                    {scenarioReason ||
                      busyRun ||
                      "Cloud workspace only. Each attempt saves a fresh compatible variant."}
                  </p>
                  <div class="card-actions mt-1">
                    <AttemptButton
                      template={template.id}
                      scenarioID={scenario.id}
                      label="New scenario"
                      name={`New scenario: ${scenario.title}`}
                      icon={Shuffle}
                      blocked={scenarioReason || busyRun}
                      describedby={`scenario-${scenario.id}-help`}
                    />
                  </div>
                </div>
              </li>
            {/each}
          </ul>
        </section>
        <section aria-labelledby="template-attempts-title">
          <h3 id="template-attempts-title" class="text-sm font-semibold">
            Your attempts on this revision
          </h3>
          {#if catalogError && catalog}<p class="mt-2 text-xs text-warning" role="status">
              Updates are unavailable. Showing your last loaded attempts.
            </p>{/if}
          {#if !catalog && catalogError}<p class="mt-2 text-sm text-warning" role="status">
              Your attempts could not be loaded ({catalogError}).
            </p>
            {#if retryCatalog}<button class="btn btn-sm min-h-11 mt-3" onclick={retryCatalog}
                ><RotateCw size={15} />Retry attempts</button
              >{/if}
          {:else if !catalog}<p class="mt-2 text-sm text-base-content/70" role="status">
              Loading…
            </p>
          {:else if mine.length}<ul
              class="rounded-box border border-base-content/9 bg-base-200/70 mt-3 divide-y divide-base-content/8"
            >
              {#each mine as lab (lab.id)}
                {@const done = lab.stepCount > 0 && lab.completedSteps >= lab.stepCount}
                <li class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
                  <div class="min-w-0 flex-1 basis-48 text-sm">
                    <p class="font-medium">
                      {lab.scenarioID ? scenarioTitle(lab.scenarioID) : "Baseline practice"}
                    </p>
                    <p class="text-xs text-base-content/70">
                      Started {date(lab.publishedAt, { dateStyle: "medium", timeStyle: "short" })}
                      · {done ? "Complete" : `${lab.completedSteps} of ${lab.stepCount} stages`}
                    </p>
                  </div>
                  <a
                    class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                    href={learnPath(lab.id)}
                    aria-label={`${done ? "Review" : "Continue"} ${lab.scenarioID ? scenarioTitle(lab.scenarioID) : "baseline practice"} attempt from ${date(lab.publishedAt, { dateStyle: "medium", timeStyle: "short" })}`}
                    >{done ? "Review" : "Continue"}<ArrowRight size={15} /></a
                  >
                </li>
              {/each}
            </ul>
          {:else}<p class="mt-2 text-sm text-base-content/70">
              No attempts on this revision yet.
            </p>{/if}
          {#if earlier.length}<p class="mt-3 text-xs text-base-content/60">
              Earlier published labs:
              {#each earlier as lab, i (lab.id)}{#if i}, {/if}<a
                  class="link link-hover"
                  href={learnPath(lab.id)}>{lab.title}</a
                >{/each}
            </p>{/if}
        </section>
        <section aria-labelledby="template-stages-title">
          {#if template.handover}<details
              class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mb-6"
            >
              <summary class="collapse-title min-h-11 text-sm font-medium"
                >Environment handover</summary
              >
              <div class="collapse-content">
                <div class={prose}>{@html render(template.handover)}</div>
              </div>
            </details>{/if}
          <h3 id="template-stages-title" class="mb-3 text-sm font-semibold">Stage preview</h3>
          {#if template.stages.length}
            <div class="flex flex-wrap items-center gap-3">
              <label class="text-sm text-base-content/70" for="template-preview-stage">Stage</label>
              <select
                id="template-preview-stage"
                class="select select-sm min-h-11 min-w-0 flex-1 lg:min-h-9"
                value={stage?.id}
                onchange={(e) => (selected = e.currentTarget.value)}
              >
                {#each template.stages as s, i (s.id)}<option value={s.id}
                    >{i + 1}. {s.title}</option
                  >{/each}
              </select>
            </div>
            {#if stage}<article class="mt-6" aria-label="Stage preview">
                <p class="text-xs font-medium text-base-content/70">
                  {kindLabel(stage.kind)}{#if !template.practiceStageIDs.includes(stage.id)}{stage.kind !== "instructor" && scenarioList(template).length
                      ? " · Scenario or meeting"
                      : " · Meeting only"}{/if}
                </p>
                <h4 class="mt-1 text-xl font-semibold tracking-tight">{stage.title}</h4>
                <div
                  class={[prose, "mt-4 break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto"]}
                >
                  {@html render(stage.instructions)}
                </div>
                {#if stage.prompt}<p
                    class="rounded-box border border-base-content/9 bg-base-200/70 mt-5 p-4 text-sm leading-relaxed"
                  >
                    {stage.prompt}
                  </p>{/if}
              </article>{/if}
          {:else}<p class="text-sm text-base-content/70">This revision has no stages.</p>{/if}
        </section>
      </div>
    </div>
  {:else if detailFailure}<Notice message={detailFailure} /><button
      class="btn btn-sm min-h-11"
      onclick={() => detail(id)}><RotateCw size={15} />Retry template</button
    >
  {:else}<div role="status" class="space-y-4">
      <div class="skeleton h-7 w-64 max-w-full"></div>
      <div class="skeleton h-40 w-full"></div>
      <span class="sr-only">Loading template…</span>
    </div>{/if}
{:else}
  <section aria-label="Template library">
    <div class="flex flex-wrap items-center gap-2">
      <label class="input input-sm min-h-11 w-full sm:max-w-xs lg:min-h-9"
        ><Search size={15} aria-hidden="true" class="opacity-60" /><input
          id="template-search"
          type="search"
          aria-label="Search templates"
          value={query}
          oninput={(e) => set("q", e.currentTarget.value)}
          placeholder="Search templates, tags, scenarios"
          autocomplete="off"
        /></label
      >
      {#if categories.length > 1}<select
          class="select select-sm min-h-11 w-full sm:w-auto lg:min-h-9"
          aria-label="Category"
          value={category}
          onchange={(e) => set("category", e.currentTarget.value)}
          ><option value="">All categories</option>{#each categories as c (c)}<option value={c}
              >{c}</option
            >{/each}</select
        >{/if}
      {#if difficulties.length > 1}<select
          class="select select-sm min-h-11 w-full sm:w-auto lg:min-h-9"
          aria-label="Difficulty"
          value={difficulty}
          onchange={(e) => set("difficulty", e.currentTarget.value)}
          ><option value="">Any difficulty</option>{#each difficulties as d (d)}<option value={d}
              >{difficultyLabel(d)}</option
            >{/each}</select
        >{/if}
      {#if anyScenarios}<select
          class="select select-sm min-h-11 w-full sm:w-auto lg:min-h-9"
          aria-label="Practice mode"
          value={mode}
          onchange={(e) => set("mode", e.currentTarget.value)}
          ><option value="">All practice modes</option><option value="guided"
            >Baseline practice</option
          ><option value="scenarios">Randomized scenarios</option></select
        >{/if}
    </div>
    {#if failure && templates}<p class="mt-4 text-sm text-warning" role="status">
        Updates are unavailable ({failure}). Showing the last loaded library.
      </p>{/if}
    {#if templates}
      {#if filtered}<p class="sr-only" role="status">
          {visible.length} of {latest.length} templates shown
        </p>{/if}
      <ul class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {#each visible as t (t.id)}
          {@const meta = t.library}
          {@const count = scenarioList(t).length}
          <li
            class="card card-sm border border-base-content/9 bg-base-200/70 min-w-0 transition-colors hover:border-base-content/20"
          >
            <div class="card-body">
              {#if meta?.category}<p class="grow-0 text-xs font-medium text-base-content/70">
                  {meta.category}
                </p>{/if}
              <h2 class="card-title break-words text-base leading-snug">
                <a class="hover:underline" href={path(t.id)}>{t.name}</a>
              </h2>
              <p class="line-clamp-2 text-sm leading-relaxed text-base-content/70">
                {t.description || "A pinned lab environment."}
              </p>
              {#if meta?.difficulty || meta?.estimatedMinutes || meta?.tags?.length || t.coachOnly}<div
                  class="flex flex-wrap gap-1.5"
                >
                  {#if t.coachOnly}<span class="badge badge-sm badge-ghost">Coach-only</span>{/if}
                  {#if meta?.difficulty}<span class="badge badge-sm badge-soft"
                      ><Gauge size={12} aria-hidden="true" />{difficultyLabel(meta.difficulty)}</span
                    >{/if}
                  {#if meta?.estimatedMinutes}<span class="badge badge-sm badge-soft"
                      ><Clock size={12} aria-hidden="true" />{minutes(meta.estimatedMinutes)}</span
                    >{/if}
                  {#each meta?.tags ?? [] as tag (tag)}<span class="badge badge-sm badge-ghost"
                      >{tag}</span
                    >{/each}
                </div>{/if}
              <div class="mt-auto space-y-1 pt-3 text-xs text-base-content/70">
                <p>
                  {t.machines.length}
                  {t.machines.length === 1 ? "machine" : "machines"} · {t.stageCount}
                  {t.stageCount === 1 ? "stage" : "stages"}
                </p>
                <p class="flex flex-wrap gap-x-3 gap-y-1">
                  {#if practicable(t)}<span class="inline-flex items-center gap-1"
                      ><ListChecks size={12} aria-hidden="true" />Baseline practice</span
                    >{/if}
                  {#if randomized(t)}<span class="inline-flex items-center gap-1"
                      ><Shuffle size={12} aria-hidden="true" />{count}
                      {count === 1 ? "scenario" : "scenarios"}{#if !desktopWorkstation(t)}{" · needs a desktop workstation"}{:else if !t.imagesBuilt}{" · images not built"}{/if}</span
                    >{/if}
                  {#if !practicable(t) && !randomized(t)}<span>Preview only</span>{/if}
                </p>
              </div>
              <div class="card-actions justify-end pt-1">
                <a
                  class="btn btn-sm min-h-11 lg:min-h-8"
                  href={path(t.id)}
                  aria-label={`Explore ${t.name} revision ${t.commit.slice(0, 7)}`}
                  >Explore<ArrowRight size={15} /></a
                >
              </div>
            </div>
          </li>
        {:else}<li class="py-10 text-center sm:col-span-2 xl:col-span-3">
            <BookOpen size={22} class="mx-auto text-base-content/60" aria-hidden="true" />
            <h2 class="mt-3 font-semibold">
              {templates.length ? "No matching templates" : "No templates yet"}
            </h2>
            {#if !templates.length}<p class="mt-1 text-sm text-base-content/70">
                {#if $session?.user.admin}Import revisions from <a
                    class="link"
                    href="/admin/templates/">Lab templates</a
                  >.{:else}Templates appear here once an instructor imports them.{/if}
              </p>{/if}
            {#if filtered}<button class="btn btn-sm btn-ghost min-h-11 mt-3" onclick={clear}
                >Clear filters</button
              >{/if}
          </li>{/each}
      </ul>
    {:else if failure}<Notice message={failure} /><button
        class="btn btn-sm min-h-11"
        onclick={load}><RotateCw size={15} />Retry library</button
      >
    {:else}<div class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3" role="status">
        {#each [1, 2, 3] as n (n)}<div class="skeleton h-44 w-full rounded-box"></div>{/each}<span
          class="sr-only">Loading templates…</span
        >
      </div>{/if}
  </section>
{/if}
