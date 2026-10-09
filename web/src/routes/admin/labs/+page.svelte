<script lang="ts">
  import { onMount, untrack, tick } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import { render, prose } from "$lib/markdown";
  import {
    ArrowDown,
    ArrowUp,
    BookOpen,
    Eye,
    Pencil,
    Plus,
    RotateCw,
    Send,
    Trash2,
    LockOpen,
    EyeOff,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import PageHeader from "$lib/PageHeader.svelte";
  import AdminNav from "$lib/AdminNav.svelte";
  import Notice from "$lib/Notice.svelte";
  import PollStatus from "$lib/PollStatus.svelte";
  import {
    session,
    api,
    APIError,
    adminView,
    refreshAdmin,
    refresh,
    date,
  } from "$lib/api";
  import {
    sessionScope,
    learnPath,
    modeLabel,
    kindLabel,
    hours,
  } from "$lib/learning";
  import type {
    LearningAdminView,
    LearningPublication,
    LearningStepInput,
    PublishLearningRequest,
    Template,
  } from "$lib/types";

  const maxSteps = 20;
  let admin = $state<LearningAdminView | null>(null);
  let failure = $state("");
  let error = $state("");
  let success = $state("");
  let busy = $state(false);
  let scope = "";
  let seq = 0;
  let pending = $state<{
    kind: "release" | "unpublish";
    publication: LearningPublication;
    released: number;
  } | null>(null);
  let dialog: HTMLDialogElement;
  let titleInput: HTMLInputElement;

  function nextID(steps: LearningStepInput[]) {
    let n = 1;
    while (steps.some((s) => s.id === `step-${n}`)) n++;
    return `step-${n}`;
  }
  function newStep(steps: LearningStepInput[]): LearningStepInput {
    return {
      id: nextID(steps),
      title: "",
      instructions: "",
      kind: "answer",
      prompt: "",
      answer: "",
      checkID: "",
    };
  }
  function blank(): PublishLearningRequest {
    return {
      template: "",
      title: "",
      description: "",
      mode: "self-paced",
      estimatedMinutes: 30,
      steps: [newStep([])],
      stageIDs: [],
    };
  }
  let draft = $state<PublishLearningRequest>(blank());
  let attempted = $state(false);

  async function load() {
    const s = scope;
    const n = ++seq;
    if (!s) return;
    try {
      const next = await api<LearningAdminView>("/api/admin/learning");
      if (n !== seq || s !== scope) return;
      admin = next;
      failure = "";
    } catch (e) {
      if (n !== seq || s !== scope) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        admin = null;
        void refresh();
      }
      failure = (e as Error).message;
    }
  }
  $effect(() => {
    const s = $session?.user.admin ? sessionScope($session) : "";
    untrack(() => {
      if (s === scope) return;
      scope = s;
      seq++;
      admin = null;
      failure = "";
      error = "";
      success = "";
      draft = blank();
      attempted = false;
      if (s) void load();
    });
  });
  let mounted = $state(false);
  onMount(() => {
    mounted = true;
    void refreshAdmin().catch(() => {});
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, 30000);
    return () => clearInterval(timer);
  });

  const templates = $derived($adminView?.templates ?? []);
  $effect(() => {
    if (!draft.template && templates.length) {
      const requested = browser && mounted ? page.url.searchParams.get("template") : null;
      draft.template = (templates.find((t) => t.id === requested) ?? templates.find((t) => t.built) ?? templates[0]).id;
    }
  });
  const template = $derived(templates.find((t) => t.id === draft.template));
  const authored = $derived(template?.instructor?.stages ?? []);
  const ownedStages = $derived(authored.length > 0);
  const eligible = $derived(authored.filter(s => s.kind !== "instructor" && !s.exerciseID));
  let selectedRevision = "";
  $effect(() => {
    const revision = draft.template;
    if (revision !== selectedRevision) {
      selectedRevision = revision;
      draft.stageIDs = eligible.map(s => s.id);
      if (ownedStages) draft.mode = "self-paced";
      attempted = false;
    }
  });
  const checks = $derived(template?.instructor?.checks ?? []);
  function templateLabel(id: string) {
    const t = templates.find((t) => t.id === id);
    return t
      ? `${t.name} · ${t.commit.slice(0, 7)}`
      : `Revision ${id.slice(0, 12)}`;
  }
  function optionLabel(t: Template) {
    return `${t.name} · ${t.commitTitle || "Imported revision"} · ${t.commit.slice(0, 7)} · ${t.built ? "images built" : "not built"}`;
  }
  function problems(request: PublishLearningRequest) {
    const out: string[] = [];
    if (!request.template) out.push("Choose a template revision.");
    if (!request.title.trim()) out.push("Add a lab title.");
    if (request.description.length > 2000)
      out.push("Keep the description under 2000 characters.");
    if (
      !Number.isInteger(request.estimatedMinutes) ||
      request.estimatedMinutes < 1 ||
      request.estimatedMinutes > 1440
    )
      out.push("Estimated time must be 1–1440 minutes.");
    if (ownedStages) {
      const ids = request.stageIDs ?? [];
      if (!ids.length) out.push("Select at least one practice stage from this revision.");
      if (ids.some(id => !eligible.some(s => s.id === id))) out.push("Choose only stages available for personal practice.");
      return out;
    }
    if (request.steps.length < 1 || request.steps.length > maxSteps)
      out.push(`Use 1–${maxSteps} stages.`);
    request.steps.forEach((step, i) => {
      const name = `Stage ${i + 1}`;
      if (!step.title.trim()) out.push(`${name} needs a title.`);
      if (!step.instructions.trim()) out.push(`${name} needs instructions.`);
      if (step.instructions.length > 6000)
        out.push(`${name} instructions must be under 6000 characters.`);
      if (step.prompt.length > 1000)
        out.push(`${name} prompt must be under 1000 characters.`);
      if (step.kind === "answer" && !step.answer.trim())
        out.push(`${name} needs an accepted answer.`);
      if (step.kind === "action" && !checks.some((c) => c.id === step.checkID))
        out.push(`${name} needs a check from this revision.`);
      if (step.kind === "instructor" && request.mode !== "instructor")
        out.push(`${name} is a legacy instructor checkpoint; choose an answer or workspace check for new practice.`);
    });
    return out;
  }
  const issues = $derived(problems(draft));

  function move(index: number, by: number) {
    const steps = draft.steps;
    const [step] = steps.splice(index, 1);
    steps.splice(index + by, 0, step);
  }
  async function editAsNew(publication: LearningPublication) {
    draft = {
      template: publication.template,
      title: publication.title,
      description: publication.description,
      mode: "self-paced",
      estimatedMinutes: publication.estimatedMinutes,
      steps: publication.steps.map((s) => ({ ...s })),
      stageIDs: publication.steps.map(s => s.id),
    };
    attempted = false;
    success = "";
    await tick();
    titleInput?.scrollIntoView({ block: "center" });
    titleInput?.focus();
  }
  async function publish(event: SubmitEvent) {
    event.preventDefault();
    attempted = true;
    if (issues.length || busy) return;
    const s = scope;
    busy = true;
    error = "";
    success = "";
    const request: PublishLearningRequest = {
      ...$state.snapshot(draft),
      title: draft.title.trim(),
      mode: "self-paced",
      stageIDs: ownedStages ? eligible.filter(s => draft.stageIDs?.includes(s.id)).map(s => s.id) : [],
      steps: ownedStages ? [] : draft.steps.map((step) => ({
        ...$state.snapshot(step),
        answer: step.kind === "answer" ? step.answer : "",
        checkID: step.kind === "action" ? step.checkID : "",
      })),
    };
    try {
      const result = await api<LearningPublication>(
        "/api/admin/learning/publish",
        request,
      );
      if (s !== scope) return;
      success = `Published “${result.title}”. Students can open it from Labs.`;
      draft = blank();
      attempted = false;
    } catch (e) {
      if (s === scope) error = (e as Error).message;
    } finally {
      busy = false;
      void load();
    }
  }
  function confirm(
    kind: "release" | "unpublish",
    publication: LearningPublication,
    released = publication.releasedSteps,
  ) {
    pending = { kind, publication, released };
    error = "";
    dialog.showModal();
  }
  async function apply() {
    if (!pending || busy) return;
    const s = scope;
    busy = true;
    error = "";
    success = "";
    try {
      if (pending.kind === "release")
        await api("/api/admin/learning/release", {
          id: pending.publication.id,
          releasedSteps: pending.released,
        });
      else
        await api("/api/admin/learning/unpublish", {
          id: pending.publication.id,
        });
      if (s !== scope) return;
      success =
        pending.kind === "release"
          ? `Released ${pending.released} of ${pending.publication.steps.length} stages of “${pending.publication.title}”.`
          : `Unpublished “${pending.publication.title}”.`;
      dialog.close();
    } catch (e) {
      if (s === scope) error = (e as Error).message;
    } finally {
      busy = false;
      void load();
    }
  }
  const ordered = $derived(
    (admin?.labs ?? []).toSorted(
      (a, b) =>
        Number(b.published) - Number(a.published) ||
        Date.parse(b.publishedAt) - Date.parse(a.publishedAt),
    ),
  );
</script>

<svelte:head><title>Lab publishing · LabCentral</title></svelte:head>
<Guard admin>
  <AdminNav />
  <PageHeader
    title="Lab publishing"
    description="Publish guided labs from pinned template revisions. Publications cannot be edited; publish a new version instead."
  />
  <Notice message={error} dismissible /><PollStatus />
  {#if success}<p class="mb-4 text-sm" role="status">{success}</p>{/if}
  {#if admin}
    <p class="mb-4 text-sm text-base-content/75">
      Each student has {hours(admin.quotaMinutes * 60)} of cloud workspace time per
      UTC week. Local downloads do not use it.
    </p>
    <section aria-labelledby="publications-title">
      <h2 id="publications-title" class="text-base font-semibold">Publications</h2>
      <ul
        class="mt-3 divide-y divide-base-content/10 border-y border-base-content/10"
      >
        {#each ordered as publication (publication.id)}
          {@const total = publication.steps.length}
          <li class="grid gap-4 py-5 md:grid-cols-[minmax(0,1fr)_auto]">
            <div class="min-w-0">
              <div
                class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-base-content/75"
              >
                <span class="badge badge-ghost badge-sm"
                  >{publication.published ? "Published" : "Unpublished"}</span
                >
                <span>{modeLabel(publication.mode)}</span>
                <span>About {publication.estimatedMinutes} min</span>
                <span>{total} {total === 1 ? "stage" : "stages"}</span>
                <span
                  >{templateLabel(publication.template)} · {date(
                    publication.publishedAt,
                    { dateStyle: "medium", timeStyle: "short" },
                  )}</span
                >
              </div>
              <h3 class="mt-2 text-base font-semibold">{publication.title}</h3>
              {#if publication.description}<p
                  class="mt-1 line-clamp-2 max-w-[68ch] text-sm text-base-content/75"
                >
                  {publication.description}
                </p>{/if}
              {#if publication.mode === "instructor"}
                <div class="mt-3 max-w-md">
                  <p class="text-sm tabular-nums">
                    Released {publication.releasedSteps} of {total} stages
                  </p>
                  <progress
                    class="progress mt-1.5 h-1 w-full"
                    value={publication.releasedSteps}
                    max={total || 1}
                    aria-label={`${publication.title} stages released`}
                  ></progress>
                  {#if publication.releasedSteps < total}
                    <p class="mt-1.5 text-xs text-base-content/75">
                      Next: {publication.steps[publication.releasedSteps].title}
                      · {kindLabel(publication.steps[publication.releasedSteps].kind)}
                    </p>
                  {/if}
                </div>
              {/if}
            </div>
            <div class="flex flex-wrap items-start gap-2 md:justify-end">
              {#if publication.published && publication.mode === "instructor" && publication.releasedSteps < total}<button
                  type="button"
                  class="btn btn-sm min-h-11 lg:min-h-8"
                  disabled={busy}
                  onclick={() =>
                    confirm(
                      "release",
                      publication,
                      publication.releasedSteps + 1,
                    )}
                  ><LockOpen size={14} />Release stage {publication.releasedSteps +
                    1}</button
                >{/if}
              {#if publication.published}<a
                  class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                  href={learnPath(publication.id)}
                  ><Eye size={14} />View as student</a
                >{/if}
              <button
                type="button"
                class="btn btn-sm btn-ghost min-h-11 lg:min-h-8"
                disabled={busy}
                onclick={() => editAsNew(publication)}
                ><Pencil size={14} />Edit as new</button
              >
              {#if publication.published}<button
                  type="button"
                  class="btn btn-sm btn-ghost min-h-11 text-error lg:min-h-8"
                  disabled={busy}
                  onclick={() => confirm("unpublish", publication)}
                  ><EyeOff size={14} />Unpublish</button
                >{/if}
            </div>
          </li>
        {:else}
          <li class="py-8">
            <BookOpen size={22} class="text-base-content/75" aria-hidden="true" />
            <p class="mt-3 font-semibold">No labs published yet</p>
            <p class="mt-1 text-sm text-base-content/75">
              Use the editor below to publish the first lab.
            </p>
          </li>
        {/each}
      </ul>
    </section>
  {:else if failure}
    <Notice message={failure} />
    <button class="btn btn-sm min-h-11" onclick={() => load()}
      ><RotateCw size={15} />Retry</button
    >
  {:else}
    <p class="flex items-center gap-2 text-sm text-base-content/75" role="status">
      <span class="loading loading-spinner loading-sm"></span>Loading
      publications…
    </p>
  {/if}

  <form class="mt-12 max-w-3xl" onsubmit={publish} novalidate>
    <h2 class="text-base font-semibold" id="editor-title">Publish a lab</h2>
    <p class="mt-1 text-sm text-base-content/75">
      Choose authored stages from a pinned template revision. Students complete
      published practice in order; meeting exercises remain freely selectable.
    </p>
    <fieldset class="fieldset mt-4 gap-2 text-sm" disabled={busy}>
      <legend class="sr-only">Lab details</legend>
      <label class="label" for="publish-template">Template revision</label>
      <select
        id="publish-template"
        class="select select-sm min-h-11 w-full"
        bind:value={draft.template}
        aria-describedby="publish-template-help"
        ><option value="" disabled>Choose a revision</option
        >{#each templates as t (t.id)}<option value={t.id}>{optionLabel(t)}</option
          >{/each}</select
      >
      <p id="publish-template-help" class="text-xs text-base-content/75">
        {#if !templates.length}No revisions are imported yet. Refresh them in <a
            class="link"
            href="/admin/templates/">Templates</a
          >.{:else if template && !template.built}Local downloads work now. Cloud
          workspaces need built images with a workstation desktop.{:else}Cloud
          workspaces need built images with a workstation desktop; local
          downloads do not.{/if}
      </p>
      <label class="label mt-3" for="publish-title">Title</label>
      <input
        id="publish-title"
        class="input input-sm min-h-11 w-full"
        bind:value={draft.title}
        bind:this={titleInput}
        maxlength="100"
        required
      />
      <label class="label mt-3" for="publish-description">Description</label>
      <textarea
        id="publish-description"
        class="textarea textarea-sm w-full"
        rows="2"
        maxlength="2000"
        bind:value={draft.description}
      ></textarea>
      <div class="mt-3 grid gap-4 sm:grid-cols-2">
        <div><p class="label mb-2">Pacing</p><p class="flex min-h-11 items-center text-sm">Self-paced practice</p><p class="mt-1 text-xs text-base-content/75">All stages open on publish. Meeting stages are selected in Manage meeting.</p></div>
        <div>
          <label class="label mb-2" for="publish-minutes">Estimated minutes</label>
          <input
            id="publish-minutes"
            class="input input-sm min-h-11 w-full"
            type="number"
            min="1"
            max="1440"
            step="1"
            bind:value={draft.estimatedMinutes}
            required
          />
        </div>
      </div>
    </fieldset>
    {#if ownedStages}
      <fieldset class="mt-8" disabled={busy}>
        <legend class="text-sm font-semibold">Template stages</legend>
        <p class="mt-2 text-xs leading-relaxed text-base-content/75">Selected stages keep their authored instructions, completion checks and solutions. Publishing snapshots revision {template?.commit.slice(0, 7)}.</p>
        <ul class="mt-3 divide-y divide-base-content/10 border-y border-base-content/10">
          {#each authored as stage (stage.id)}
            {@const available = stage.kind !== "instructor" && !stage.exerciseID}
            <li class="py-4">
              <label class="flex min-h-11 items-start gap-3"><input class="checkbox checkbox-sm mt-1 shrink-0" type="checkbox" value={stage.id} bind:group={draft.stageIDs} disabled={!available} /><span class="min-w-0"><span class="block text-sm font-medium">{stage.title}</span><span class="mt-1 block text-xs text-base-content/75">{kindLabel(stage.kind)}{!available ? " · Meeting only" : " · Personal practice"}</span></span></label>
              {#if !available}<p class="ml-8 text-xs leading-relaxed text-base-content/75">{stage.exerciseID ? "This stage needs an instructor-applied break during a meeting; personal practice cannot apply it yet." : "Instructor checkpoints are available during meetings."}</p>{/if}
              <details class="collapse collapse-arrow mt-2 border border-base-content/10"><summary class="collapse-title text-sm">Review authored stage</summary><div class="collapse-content"><div class={prose}>{@html render(stage.instructions)}</div>{#if stage.prompt}<p class="mt-3 text-sm">{stage.prompt}</p>{/if}{#if stage.solution}<p class="mt-4 text-xs font-medium">Instructor solution</p><div class={[prose, "mt-2"]}>{@html render(stage.solution)}</div>{/if}</div></details>
            </li>
          {/each}
        </ul>
        <p class="mt-3 text-xs tabular-nums text-base-content/75">{draft.stageIDs?.length ?? 0} of {eligible.length} practice stages selected · template order is preserved</p>
      </fieldset>
    {:else}
      <div class="mt-8 border-t border-base-content/10 pt-5"><h3 class="text-sm font-semibold">Legacy manual lesson</h3><p class="mt-2 text-xs leading-relaxed text-base-content/75">This revision has no template-owned stages. Write an independent lesson below, or import a revision with authored stages.</p></div>
    <div class="mt-8 flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="text-sm font-semibold">Stages</h3>
      <p class="text-xs tabular-nums text-base-content/75">
        {draft.steps.length} of {maxSteps}
      </p>
    </div>
    <ol class="mt-2 divide-y divide-base-content/10 border-y border-base-content/10">
      {#each draft.steps as step, index (step.id)}
        <li class="py-5">
          <fieldset class="fieldset gap-2 text-sm" disabled={busy}>
            <legend class="sr-only">Stage {index + 1}</legend>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <p class="font-medium">
                Stage {index + 1}
                <span class="ml-2 text-xs font-normal text-base-content/75"
                  >ID <code>{step.id}</code></span
                >
              </p>
              <div class="flex gap-1">
                <button
                  type="button"
                  class="btn btn-sm btn-ghost btn-square min-h-11 min-w-11 lg:min-h-8 lg:min-w-8"
                  aria-label={`Move stage ${index + 1} up`}
                  title="Move up"
                  disabled={index === 0}
                  onclick={() => move(index, -1)}><ArrowUp size={15} /></button
                >
                <button
                  type="button"
                  class="btn btn-sm btn-ghost btn-square min-h-11 min-w-11 lg:min-h-8 lg:min-w-8"
                  aria-label={`Move stage ${index + 1} down`}
                  title="Move down"
                  disabled={index === draft.steps.length - 1}
                  onclick={() => move(index, 1)}><ArrowDown size={15} /></button
                >
                <button
                  type="button"
                  class="btn btn-sm btn-ghost btn-square min-h-11 min-w-11 lg:min-h-8 lg:min-w-8"
                  aria-label={`Remove stage ${index + 1}`}
                  title={draft.steps.length === 1
                    ? "A lab needs at least one stage"
                    : "Remove stage"}
                  disabled={draft.steps.length === 1}
                  onclick={() => draft.steps.splice(index, 1)}
                  ><Trash2 size={15} /></button
                >
              </div>
            </div>
            <div class="grid gap-3 sm:grid-cols-[minmax(0,1fr)_14rem]">
              <div>
                <label class="label mb-1.5" for={`${step.id}-title`}>Stage title</label>
                <input
                  id={`${step.id}-title`}
                  aria-label={`Stage title, stage ${index + 1}`}
                  class="input input-sm min-h-11 w-full"
                  bind:value={step.title}
                  maxlength="100"
                />
              </div>
              <div>
                <label class="label mb-1.5" for={`${step.id}-kind`}>Completion</label>
                <select
                  id={`${step.id}-kind`}
                  aria-label={`Completion, stage ${index + 1}`}
                  class="select select-sm min-h-11 w-full"
                  bind:value={step.kind}
                >
                  <option value="answer">Answer</option>
                  <option value="action" disabled={!checks.length}
                    >Workspace check{checks.length ? "" : " (no checks in revision)"}</option
                  >
                  {#if step.kind === "instructor"}<option value="instructor" disabled>Legacy checkpoint · choose answer or check</option>{/if}
                </select>
              </div>
            </div>
            <label class="label mt-2" for={`${step.id}-instructions`}
              >Instructions</label
            >
            <textarea
              id={`${step.id}-instructions`}
              aria-label={`Instructions, stage ${index + 1}`}
              class="textarea textarea-sm w-full font-mono text-xs leading-relaxed"
              rows="6"
              maxlength="6000"
              bind:value={step.instructions}
              aria-describedby={`${step.id}-instructions-help`}
            ></textarea>
            <p id={`${step.id}-instructions-help`} class="text-xs text-base-content/75">
              Markdown. Raw HTML is not rendered.
            </p>
            <label class="label mt-2" for={`${step.id}-prompt`}
              >{step.kind === "answer"
                ? "Question"
                : step.kind === "action"
                  ? "What to do in the workspace"
                  : "Checkpoint guidance"} (optional)</label
            >
            <textarea
              id={`${step.id}-prompt`}
              aria-label={`${step.kind === "answer"
                ? "Question"
                : step.kind === "action"
                  ? "What to do in the workspace"
                  : "Checkpoint guidance"} (optional), stage ${index + 1}`}
              class="textarea textarea-sm w-full"
              rows="2"
              maxlength="1000"
              bind:value={step.prompt}
            ></textarea>
            {#if step.kind === "answer"}
              <label class="label mt-2" for={`${step.id}-answer`}
                >Accepted answer (private)</label
              >
              <input
                id={`${step.id}-answer`}
                aria-label={`Accepted answer (private), stage ${index + 1}`}
                class="input input-sm min-h-11 w-full"
                bind:value={step.answer}
                maxlength="1000"
                autocomplete="off"
                aria-describedby={`${step.id}-answer-help`}
              />
              <p id={`${step.id}-answer-help`} class="text-xs text-base-content/75">
                Compared exactly after trimming surrounding spaces. Never sent
                to students.
              </p>
            {:else if step.kind === "action"}
              <label class="label mt-2" for={`${step.id}-check`}
                >Verifying check</label
              >
              <select
                id={`${step.id}-check`}
                aria-label={`Verifying check, stage ${index + 1}`}
                class="select select-sm min-h-11 w-full"
                bind:value={step.checkID}
                ><option value="" disabled>Choose a check</option
                >{#each checks as c (c.id)}<option value={c.id}
                    >{c.name} · {c.points} points</option
                  >{/each}</select
              >
              <p class="text-xs text-base-content/75">
                Runs the revision's instructor check in the student's cloud
                workspace.
              </p>
            {:else}
              <p class="mt-2 text-xs text-base-content/75">
                Students complete this checkpoint after you release it.
              </p>
            {/if}
          </fieldset>
        </li>
      {/each}
    </ol>
    <button
      type="button"
      class="btn btn-sm btn-ghost min-h-11 mt-3"
      disabled={busy || draft.steps.length >= maxSteps}
      title={draft.steps.length >= maxSteps
        ? `Labs are limited to ${maxSteps} stages`
        : undefined}
      onclick={() => draft.steps.push(newStep(draft.steps))}
      ><Plus size={15} />Add stage</button
    >
    {/if}
    {#if attempted && issues.length}<div
        class="alert alert-soft alert-error mt-6 text-sm"
        role="alert"
      >
        <ul class="[list-style-type:disc] [list-style-position:inside] space-y-1">
          {#each issues as issue}<li>{issue}</li>{/each}
        </ul>
      </div>{/if}
    <div class="mt-6 flex flex-wrap items-center gap-3 border-t border-base-content/10 pt-5">
      <button class="btn btn-sm btn-primary min-h-11" disabled={busy}
        >{#if busy}<span class="loading loading-spinner loading-xs"></span>{:else}<Send
            size={15}
          />{/if}Publish lab</button
      >
      <button
        type="button"
        class="btn btn-sm btn-ghost min-h-11"
        disabled={busy}
        onclick={() => {
          draft = blank();
          attempted = false;
        }}>Clear editor</button
      >
      <p class="text-xs text-base-content/75">
        Publishing pins this revision and makes the lab visible in Labs.
      </p>
    </div>
  </form>
</Guard>
<dialog bind:this={dialog} class="modal" aria-labelledby="publish-confirm-title">
  <div class="modal-box">
    <h2 id="publish-confirm-title" class="text-lg font-semibold">
      {pending?.kind === "release"
        ? `Release stage ${pending.released}?`
        : "Unpublish this lab?"}
    </h2>
    <p class="mt-3 font-medium">{pending?.publication.title}</p>
    <p class="mt-3 text-sm leading-relaxed">
      {#if pending?.kind === "release"}Students can open
        “{pending.publication.steps[pending.released - 1]?.title}” immediately.
        Releases cannot be withdrawn.{:else}The lab leaves the catalog and new
        cloud workspaces and downloads stop. Students keep their progress and
        can still stop an active cloud workspace; cleanup continues.{/if}
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={busy}>Cancel</button>
      </form>
      <button
        class={[
          "btn btn-sm min-h-11",
          pending?.kind === "release" ? "btn-primary" : "btn-error",
        ]}
        disabled={busy}
        onclick={apply}
        >{#if busy}<span class="loading loading-spinner loading-xs"></span>{/if}{pending?.kind ===
        "release"
          ? "Release stage"
          : "Unpublish lab"}</button
      >
    </div>
  </div>
</dialog>
