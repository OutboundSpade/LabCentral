<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import {
    ArrowLeft,
    ArrowRight,
    BookOpen,
    Check,
    CircleCheck,
    Clock,
    Cloud,
    Copy,
    Download,
    ExternalLink,
    Hourglass,
    Laptop,
    Lock,
    Play,
    Send,
    ShieldCheck,
    Square,
    TriangleAlert,
    UserCheck,
  } from "@lucide/svelte";
  import Guard from "$lib/Guard.svelte";
  import Notice from "$lib/Notice.svelte";
  import Quota from "$lib/Quota.svelte";
  import Status from "$lib/Status.svelte";
  import Deadline from "$lib/Deadline.svelte";
  import EmptyState from "$lib/EmptyState.svelte";
  import StageRail from "$lib/StageRail.svelte";
  import { session, api, APIError, refresh, date } from "$lib/api";
  import { launchDesktop } from "$lib/launch";
  import { render, prose } from "$lib/markdown";
  import {
    sessionScope,
    learnPath,
    endpoint,
    modeLabel,
    kindLabel,
    liveRun,
    transitional,
    runLabel,
    hours,
  } from "$lib/learning";
  import type {
    LearningSolution,
    LearningView,
    LearningProgress,
    LearningCheck,
    PracticeRun,
  } from "$lib/types";

  let mounted = $state(false);
  // Query parameters are unavailable while prerendering; read them after hydration.
  const id = $derived(
    browser && mounted ? (page.url.searchParams.get("id") ?? "") : "",
  );
  let view = $state<LearningView | null>(null);
  let failure = $state("");
  let missing = $state(false);
  let error = $state("");
  let feedback = $state<{ ok: boolean; message: string } | null>(null);
  let busy = $state("");
  let answer = $state("");
  let solution = $state("");
  let solutionError = $state("");
  let solutionBusy = $state(false);
  let solutionSeq = 0;
  let selected = $state<number | null>(null);
  let workspace = $state<"cloud" | "local" | "">("");
  let copied = $state("");
  let startDialog: HTMLDialogElement;
  let stopDialog: HTMLDialogElement;
  let scope = "";
  let loadedID = "";
  let seq = 0;
  let seenCompleted = -1;
  let lastCheck = "";

  async function load() {
    const s = scope;
    const lab = loadedID;
    const n = ++seq;
    if (!s || !lab) return;
    try {
      const next = await api<LearningView>(endpoint(lab));
      if (n !== seq || s !== scope || lab !== loadedID) return;
      view = next;
      failure = "";
      missing = false;
    } catch (e) {
      if (n !== seq || s !== scope || lab !== loadedID) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        view = null;
        solution = ""; solutionError = ""; solutionSeq++; solutionBusy = false;
        void refresh();
      }
      if (e instanceof APIError && e.status === 404) {
        view = null;
        missing = true;
      }
      failure = (e as Error).message;
    }
  }
  $effect(() => {
    const s = sessionScope($session);
    const lab = id;
    untrack(() => {
      if (s === scope && lab === loadedID) return;
      scope = s;
      loadedID = lab;
      seq++;
      view = null;
      failure = "";
      missing = false;
      error = "";
      feedback = null;
      answer = "";
      solution = ""; solutionError = ""; solutionSeq++; solutionBusy = false;
      selected = null;
      workspace = "";
      seenCompleted = -1;
      lastCheck = "";
      if (s && lab) void load();
    });
  });

  const steps = $derived(view?.steps ?? []);
  const completed = $derived(
    Math.min(view?.progress.completedSteps ?? 0, steps.length),
  );
  const released = $derived(
    view
      ? view.lab.mode === "instructor"
        ? Math.min(view.releasedSteps, steps.length)
        : steps.length
      : 0,
  );
  const finished = $derived(steps.length > 0 && completed >= steps.length);
  const current = $derived(finished ? steps.length - 1 : completed);
  const shown = $derived(
    selected !== null && selected < steps.length ? selected : current,
  );
  const step = $derived(steps[shown]);
  const stepReleased = $derived(shown < released);
  const solutionKey = $derived(`${sessionScope($session)}:${id}:${step?.id ?? ""}`);
  $effect(() => {
    const key = solutionKey;
    untrack(() => { solution = ""; solutionError = ""; solutionSeq++; solutionBusy = false; });
  });
  async function revealSolution() {
    if (!view || !step || solutionBusy) return;
    const s = scope, lab = loadedID, stepID = step.id, n = ++solutionSeq;
    solutionBusy = true; solutionError = "";
    try {
      const result = await api<LearningSolution>(endpoint(lab, "solution"), {stepID});
      if (s !== scope || lab !== loadedID || stepID !== step?.id || n !== solutionSeq) return;
      if (result.stepID === stepID) solution = result.solution;
    } catch(e) {
      if (s !== scope || lab !== loadedID || stepID !== step?.id || n !== solutionSeq) return;
      if (e instanceof APIError && [401,403].includes(e.status)) { view = null; solution = ""; void refresh(); }
      solutionError = (e as Error).message;
    } finally { if (n === solutionSeq) solutionBusy = false; }
  }
  const isCurrent = $derived(!finished && shown === completed);
  const run = $derived(liveRun(view?.run));
  const ownRun = $derived(run && run.labID === view?.lab.id ? run : null);
  const otherRun = $derived(run && run.labID !== view?.lab.id ? run : null);
  const check = $derived(
    view?.check && step && view.check.stepID === step.id ? view.check : null,
  );
  const checkPending = $derived(
    !!view?.check && ["queued", "running"].includes(view.check.status),
  );
  const waitingRelease = $derived(
    !!view && !finished && completed >= released,
  );
  const mode = $derived(
    workspace || (ownRun || view?.cloudReady ? "cloud" : "local"),
  );
  const instructions = $derived(
    step && stepReleased ? render(step.instructions) : "",
  );

  $effect(() => {
    const c = completed;
    untrack(() => {
      if (c === seenCompleted) return;
      if (seenCompleted >= 0 && c > seenCompleted) {
        selected = null;
        answer = "";
      }
      seenCompleted = c;
    });
  });
  $effect(() => {
    const c = view?.check;
    const key = c ? `${c.id}:${c.status}` : "";
    untrack(() => {
      if (key === lastCheck) return;
      const wasPending =
        !!c &&
        (lastCheck === `${c.id}:queued` || lastCheck === `${c.id}:running`);
      lastCheck = key;
      if (wasPending && c?.status === "passed")
        feedback = {
          ok: true,
          message: "Workspace check passed. Stage complete.",
        };
    });
  });

  onMount(() => {
    mounted = true;
  });
  // Poll quickly only while provisioning, cleanup or verification is pending.
  const interval = $derived(
    checkPending || transitional(run) ? 3000 : waitingRelease ? 10000 : 20000,
  );
  $effect(() => {
    if (!mounted) return;
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, interval);
    return () => clearInterval(timer);
  });

  function choose(index: number) {
    selected = index;
    feedback = null;
    error = "";
  }
  async function act<T>(name: string, path: string, body: unknown) {
    if (busy) return null;
    const s = scope;
    const lab = loadedID;
    busy = name;
    error = "";
    try {
      const result = await api<T>(path, body);
      return s === scope && lab === loadedID ? result : null;
    } catch (e) {
      if (s === scope && lab === loadedID) error = (e as Error).message;
      return null;
    } finally {
      busy = "";
      void load();
    }
  }
  async function submitAnswer(event: SubmitEvent) {
    event.preventDefault();
    if (!view || !step) return;
    const before = completed;
    feedback = null;
    const progress = await act<LearningProgress>(
      "answer",
      endpoint(view.lab.id, "answer"),
      { stepID: step.id, answer },
    );
    if (!progress || !view) return;
    view.progress = progress;
    feedback =
      progress.completedSteps > before
        ? {
            ok: true,
            message: progress.lastFeedback || "Answer accepted.",
          }
        : {
            ok: false,
            message:
              progress.lastFeedback ||
              "That answer did not match. Review the lab and try again.",
          };
  }
  async function verify() {
    if (!view || !step) return;
    const stepID = step.id;
    const kind = step.kind;
    feedback = null;
    const result = await act<LearningCheck>(
      "check",
      endpoint(view.lab.id, "check"),
      { stepID },
    );
    if (!result || !view || result.stepID !== stepID) return;
    view.check = result;
    if (result.status === "passed")
      feedback = {
        ok: true,
        message:
          kind === "instructor"
            ? "Checkpoint complete."
            : "Workspace check passed. Stage complete.",
      };
  }
  async function start() {
    if (!view) return;
    const result = await act<PracticeRun>(
      "start",
      endpoint(view.lab.id, "start"),
      {},
    );
    if (result && view) {
      view.run = result;
      startDialog.close();
    }
  }
  async function stop() {
    if (!view) return;
    const result = await act<PracticeRun>(
      "stop",
      endpoint(view.lab.id, "stop"),
      {},
    );
    if (result && view) {
      view.run = result;
      stopDialog.close();
    }
  }
  async function openDesktop() {
    if (!view || busy) return;
    const s = scope;
    busy = "access";
    error = "";
    try {
      await launchDesktop(
        endpoint(view.lab.id, "access"),
        "Starting your practice desktop and its lesson panel…",
      );
    } catch (e) {
      if (s === scope) error = (e as Error).message;
    } finally {
      busy = "";
      void load();
    }
  }
  const commands = $derived(
    view
      ? [
          `mkdir labcentral-${view.lab.id}`,
          `tar -xzf labcentral-${view.lab.id}.tar.gz -C labcentral-${view.lab.id}`,
          `cd labcentral-${view.lab.id}`,
          "labcentral validate lab",
          'labcentral plan --environment "$HOME/.config/labcentral/aws.yaml" lab',
          'labcentral deploy --environment "$HOME/.config/labcentral/aws.yaml" lab',
          "labcentral access INSTANCE_ID MACHINE desktop",
          "labcentral destroy INSTANCE_ID",
        ].join("\n")
      : "",
  );
  async function copyCommands() {
    try {
      await navigator.clipboard.writeText(commands);
      copied = "Commands copied.";
    } catch {
      copied = "Copy is unavailable here. Select the commands to copy them.";
    }
  }
  const startBlocked = $derived(
    !view
      ? ""
      : !view.cloudReady
        ? "This revision has no built desktop image for cloud workspaces. Use the local download, or ask an instructor to build the template."
        : otherRun
          ? "Your cloud workspace for another lab is still active. Stop it before starting one here."
          : view.quota.remainingSeconds < 60
            ? `Less than a minute of weekly cloud time is left. It resets ${date(view.quota.resetsAt, { weekday: "short", hour: "numeric", minute: "2-digit" })}.`
            : "",
  );
  const verifyBlocked = $derived(
    checkPending
      ? "A workspace check is already running."
      : !ownRun
        ? "Automatic verification runs in your hosted cloud workspace. Start one to verify this stage; local runs cannot be verified here."
        : ownRun.status !== "ready"
          ? "Wait until your cloud workspace is ready."
          : "",
  );
</script>

<svelte:head
  ><title>{view ? `${view.lab.title} · ` : ""}Practice lab · LabCentral</title
  ></svelte:head
>
<Guard>
  {#if !mounted}<p class="sr-only" role="status">Loading lab…</p>
  {:else if !id}<EmptyState icon={BookOpen} title="Choose a lab">
      Open a published lab from the catalog to see its stages and workspace.
      {#snippet actions()}<a class="btn btn-sm btn-primary min-h-11" href="/labs/?view=practice"
          >Browse labs<ArrowRight size={15} /></a
        >{/snippet}
    </EmptyState>
  {:else if missing}<EmptyState icon={BookOpen} title="This lab is unavailable">
      It may have been unpublished. Your saved progress is kept; any active
      cloud workspace can still be stopped from the lab catalog.
      {#snippet actions()}<a class="btn btn-sm min-h-11" href="/labs/?view=practice"
          ><ArrowLeft size={15} />Back to labs</a
        >{/snippet}
    </EmptyState>
  {:else if view}
    <nav aria-label="Breadcrumb" class="breadcrumbs mb-1 py-0 text-sm">
      <ul>
        <li><a class="min-h-11" href="/labs/?view=practice">Labs</a></li>
        <li>
          <span class="max-w-64 truncate sm:max-w-xl" title={view.lab.title}
            >{view.lab.title}</span
          >
        </li>
      </ul>
    </nav>
    <div class="mb-6 border-b border-base-content/10 pb-5">
      <h1 class="text-2xl font-semibold tracking-tight">{view.lab.title}</h1>
      {#if view.lab.description}<p
          class="mt-1.5 max-w-[68ch] text-sm leading-relaxed text-base-content/75"
        >
          {view.lab.description}
        </p>{/if}
      <div
        class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-base-content/75"
      >
        <span class="badge badge-ghost badge-sm">{modeLabel(view.lab.mode)}</span>
        <span class="inline-flex items-center gap-1.5"
          ><Clock size={13} aria-hidden="true" />About {view.lab.estimatedMinutes}
          min</span
        >
        <span class="tabular-nums" role="status"
          >{completed} of {steps.length} stages complete</span
        >
        <progress
          class="progress h-1 w-28"
          value={completed}
          max={steps.length || 1}
          aria-label="Stages complete"
        ></progress>
      </div>
    </div>
    <Notice message={error} dismissible />
    {#if failure}<p class="mb-4 text-sm text-warning" role="status">
        Live progress updates are unavailable ({failure}). Showing your last
        loaded progress.
      </p>{/if}
    {#if !steps.length}
      <p class="text-sm">This lab has no stages yet.</p>
    {:else}
      <div
        class="grid items-start gap-8 lg:grid-cols-[minmax(0,1fr)_20rem] xl:grid-cols-[13.5rem_minmax(0,1fr)_20rem] xl:gap-10"
      >
        <nav aria-label="Lab stages" class="hidden xl:sticky xl:top-6 xl:block">
          <p class="mb-3 text-xs font-medium tracking-wide text-base-content/75 uppercase">
            Stages
          </p>
          <StageRail
            {steps}
            {completed}
            {released}
            selected={shown}
            onselect={choose}
          />
        </nav>
        <section class="min-w-0" aria-labelledby="stage-title">
          <details
            class="collapse collapse-arrow mb-5 border border-base-content/10 xl:hidden"
          >
            <summary class="collapse-title min-h-11 text-sm font-medium"
              >Stages · {completed} of {steps.length} complete</summary
            >
            <div class="collapse-content">
              <nav aria-label="Lab stages">
                <StageRail
                  {steps}
                  {completed}
                  {released}
                  selected={shown}
                  onselect={choose}
                />
              </nav>
            </div>
          </details>
          {#if finished}<div
              class="alert alert-soft alert-success mb-5 text-sm"
              role="status"
            >
              <CircleCheck size={18} aria-hidden="true" /><span
                >Lab complete. All {steps.length} stages are done; you can review
                any stage.</span
              >
            </div>{/if}
          {#if feedback}<p
              class={[
                "mb-5 flex items-start gap-2 text-sm",
                feedback.ok ? "text-success" : "text-error",
              ]}
              role={feedback.ok ? "status" : "alert"}
            >
              {#if feedback.ok}<Check
                  size={16}
                  class="mt-0.5 shrink-0"
                  aria-hidden="true"
                />{:else}<TriangleAlert
                  size={16}
                  class="mt-0.5 shrink-0"
                  aria-hidden="true"
                />{/if}{feedback.message}
            </p>{/if}
          {#if step}
            <p class="text-xs font-medium tracking-wide text-base-content/75 uppercase">
              Stage {shown + 1} of {steps.length} · {kindLabel(step.kind)}
            </p>
            <h2 id="stage-title" class="mt-1.5 text-xl font-semibold tracking-tight">
              {stepReleased ? step.title : `Stage ${shown + 1}`}
            </h2>
            {#if !stepReleased}
              <div class="mt-5 flex items-start gap-3 border-l-2 border-primary/50 py-1 pl-4 text-sm">
                <Hourglass size={17} class="mt-0.5 shrink-0" aria-hidden="true" />
                <div>
                  <p class="font-medium">Waiting for your instructor</p>
                  <p class="mt-1 leading-relaxed text-base-content/75">
                    Your instructor releases instructor-led stages in order.
                    This page checks for a release automatically; your progress
                    is saved.
                  </p>
                </div>
              </div>
            {:else}
              <article class={["mt-5", prose]}>
                {@html instructions}
              </article>
              <section
                aria-label="Stage task"
                class={[
                  "mt-8 border-l-2 py-1 pl-4",
                  isCurrent ? "border-primary/60" : "border-base-content/15",
                ]}
              >
                {#if shown < completed}
                  <p class="flex items-center gap-2 text-sm">
                    <Check size={15} aria-hidden="true" />Stage complete.
                  </p>
                {:else if !isCurrent}
                  <p class="text-sm leading-relaxed">
                    Complete stage {completed + 1} first. You can read ahead; this
                    stage's {kindLabel(step.kind).toLowerCase()} unlocks when you
                    reach it.
                  </p>
                  <button
                    type="button"
                    class="btn btn-sm btn-ghost min-h-11 mt-2"
                    onclick={() => choose(completed)}
                    ><ArrowLeft size={15} />Go to stage {completed + 1}</button
                  >
                {:else if step.kind === "answer"}
                  <form onsubmit={submitAnswer}>
                    {#if step.prompt}<p
                        id="answer-prompt"
                        class="mb-3 text-sm leading-relaxed whitespace-pre-line"
                      >
                        {step.prompt}
                      </p>{/if}
                    <label class="label mb-1.5 text-sm" for="stage-answer"
                      >Your answer</label
                    >
                    <div class="flex flex-wrap gap-2">
                      <input
                        id="stage-answer"
                        class="input input-sm min-h-11 min-w-0 flex-1 basis-56"
                        bind:value={answer}
                        maxlength="1000"
                        autocomplete="off"
                        required
                        aria-describedby={step.prompt ? "answer-prompt" : undefined}
                      />
                      <button
                        class="btn btn-sm btn-primary min-h-11"
                        disabled={!!busy || !answer.trim()}
                        >{#if busy === "answer"}<span
                            class="loading loading-spinner loading-xs"
                          ></span>{:else}<Send size={15} />{/if}Check answer</button
                      >
                    </div>
                    <p class="mt-2 text-xs text-base-content/75">
                      A correct answer completes this stage and saves your
                      progress.
                    </p>
                  </form>
                {:else if step.kind === "action"}
                  {#if step.prompt}<p
                      class="mb-3 text-sm leading-relaxed whitespace-pre-line"
                    >
                      {step.prompt}
                    </p>{/if}
                  <button
                    type="button"
                    class="btn btn-sm btn-primary min-h-11"
                    disabled={!!busy || !!verifyBlocked}
                    aria-describedby="verify-help"
                    onclick={verify}
                    >{#if busy === "check" || checkPending}<span
                        class="loading loading-spinner loading-xs"
                      ></span>{:else}<ShieldCheck size={15} />{/if}Verify in
                    workspace</button
                  >
                  <p id="verify-help" class="mt-2 text-xs leading-relaxed text-base-content/75">
                    {verifyBlocked ||
                      "Your cloud workspace is checked automatically about every 20 seconds while this stage is current; this runs a check now. A passing check completes the stage."}
                  </p>
                  {#if check}<div class="mt-4 text-sm" role="status">
                      {#if check.status === "queued" || check.status === "running"}
                        <p class="flex items-center gap-2">
                          <span class="loading loading-spinner loading-xs"></span>
                          {check.status === "queued"
                            ? "Check queued."
                            : "Checking your workspace…"}
                        </p>
                      {:else if check.status === "failed"}
                        <p class="font-medium text-error">
                          The check did not pass yet.
                        </p>
                      {:else if check.status === "unavailable"}
                        <p class="font-medium text-warning">
                          The check could not run. This is not a failed result;
                          try again when your workspace is reachable.
                        </p>
                      {:else if check.status === "passed"}
                        <p class="font-medium">Check passed.</p>
                      {/if}
                      {#if check.report}<p
                          class="mt-1.5 break-words whitespace-pre-line text-base-content/80"
                        >
                          {check.report}
                        </p>{/if}
                      <p class="mt-1 text-xs text-base-content/75">
                        Updated {date(check.updated)}
                      </p>
                    </div>{/if}
                {:else}
                  <p class="flex items-center gap-2 text-sm font-medium">
                    <UserCheck size={16} aria-hidden="true" />Instructor checkpoint
                  </p>
                  {#if step.prompt}<p
                      class="mt-2 text-sm leading-relaxed whitespace-pre-line"
                    >
                      {step.prompt}
                    </p>{/if}
                  <p class="mt-2 text-sm leading-relaxed text-base-content/75">
                    Your instructor has opened this checkpoint. Complete it when
                    you have finished this part with your group.
                  </p>
                  <button
                    type="button"
                    class="btn btn-sm btn-primary min-h-11 mt-3"
                    disabled={!!busy}
                    onclick={verify}
                    >{#if busy === "check"}<span
                        class="loading loading-spinner loading-xs"
                      ></span>{:else}<Check size={15} />{/if}Complete checkpoint</button
                  >
                {/if}
              </section>
              {#if step.hasSolution && shown <= completed}
                <section class="mt-6 border-t border-base-content/10 pt-4" aria-label="Stage solution">
                  <p class="text-xs leading-relaxed text-base-content/75">The author has included a solution for this stage. Viewing it does not complete the stage.</p>
                  {#if solution}<div class={[prose, "mt-4"]}>{@html render(solution)}</div><button class="btn btn-sm btn-ghost min-h-11 mt-2" onclick={() => solution = ""}>Hide solution</button>
                  {:else}<button type="button" class="btn btn-sm btn-ghost min-h-11 mt-2" disabled={solutionBusy} onclick={revealSolution}>{#if solutionBusy}<span class="loading loading-spinner loading-xs"></span>{:else}<BookOpen size={15} />{/if}View solution</button>{/if}
                  {#if solutionError}<p class="mt-2 text-sm text-error" role="alert">{solutionError}</p>{/if}
                </section>
              {/if}
            {/if}
            <div
              class="mt-10 flex flex-wrap items-center justify-between gap-2 border-t border-base-content/10 pt-4"
            >
              <button
                type="button"
                class="btn btn-sm btn-ghost min-h-11"
                disabled={shown === 0}
                onclick={() => choose(shown - 1)}
                ><ArrowLeft size={15} />Previous stage</button
              >
              <button
                type="button"
                class="btn btn-sm btn-ghost min-h-11"
                disabled={shown >= steps.length - 1 || shown + 1 >= released}
                onclick={() => choose(shown + 1)}
                >Next stage<ArrowRight size={15} /></button
              >
            </div>
          {/if}
        </section>
        <aside
          class="min-w-0 lg:sticky lg:top-6 lg:border-l lg:border-base-content/10 lg:pl-6"
          aria-labelledby="workspace-title"
        >
          <h2 id="workspace-title" class="text-base font-semibold">Workspace</h2>
          <div role="tablist" aria-label="Where to run this lab" class="tabs tabs-box tabs-sm mt-3">
            <button
              type="button"
              role="tab"
              class={["tab min-h-11 flex-1 gap-1.5 lg:min-h-9", mode === "cloud" ? "tab-active" : ""]}
              aria-selected={mode === "cloud"}
              aria-controls="workspace-cloud"
              id="workspace-tab-cloud"
              onclick={() => (workspace = "cloud")}
              ><Cloud size={14} aria-hidden="true" />Cloud</button
            >
            <button
              type="button"
              role="tab"
              class={["tab min-h-11 flex-1 gap-1.5 lg:min-h-9", mode === "local" ? "tab-active" : ""]}
              aria-selected={mode === "local"}
              aria-controls="workspace-local"
              id="workspace-tab-local"
              onclick={() => (workspace = "local")}
              ><Laptop size={14} aria-hidden="true" />Local</button
            >
          </div>
          <div
            id="workspace-cloud"
            role="tabpanel"
            aria-labelledby="workspace-tab-cloud"
            hidden={mode !== "cloud"}
            class="mt-5 space-y-5"
          >
            <Quota quota={view.quota} />
            <div class="border-t border-base-content/10 pt-5">
              {#if ownRun}
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <p class="text-sm font-medium">Your cloud workspace</p>
                  <span aria-live="polite"
                    ><Status status={ownRun.status} label={runLabel(ownRun.status)} /></span
                  >
                </div>
                {#if ownRun.status === "queued" || ownRun.status === "preparing"}
                  <p class="mt-2 text-sm leading-relaxed text-base-content/75">
                    Your machines are being prepared. This can take several
                    minutes; you can keep reading the instructions.
                  </p>
                {:else if ownRun.status === "ready"}
                  <button
                    type="button"
                    class="btn btn-sm btn-primary min-h-11 mt-3 w-full"
                    disabled={!!busy}
                    aria-busy={busy === "access"}
                    aria-describedby="desktop-help"
                    onclick={openDesktop}
                    >{#if busy === "access"}<span
                        class="loading loading-spinner loading-xs"
                      ></span>{:else}<ExternalLink size={15} />{/if}Open desktop</button
                  >
                  <p id="desktop-help" class="mt-2 text-xs leading-relaxed text-base-content/75">
                    Opens in a new tab with these instructions beside the
                    desktop, where you can answer and check stages too.
                    Reopening resumes the same workspace.
                  </p>
                  <div class="mt-3">
                    <Deadline
                      expires={ownRun.expires}
                      label="Ends at"
                      warning="Workspace files are deleted when this cloud run ends. Copy anything you need now."
                      due="This cloud run has reached its limit. Cleanup is due."
                    />
                  </div>
                {:else if ownRun.status === "failed"}
                  <p class="mt-2 break-words text-sm text-error">
                    {ownRun.error || "The workspace could not be prepared."}
                  </p>
                  <p class="mt-1 text-xs text-base-content/75">
                    Stop it to release its resources, then start again.
                  </p>
                {:else if ownRun.status === "ending"}
                  <p class="mt-2 text-sm leading-relaxed text-base-content/75">
                    Cleaning up. Files are deleted and quota stops counting once
                    the provider confirms removal.
                  </p>
                {/if}
                {#if ownRun.status !== "ending"}<button
                    type="button"
                    class="btn btn-sm btn-ghost min-h-11 mt-3 w-full"
                    disabled={!!busy}
                    onclick={() => {
                      error = "";
                      stopDialog.showModal();
                    }}><Square size={14} />Stop workspace</button
                  >{/if}
              {:else}
                <p class="text-sm font-medium">Personal cloud desktop</p>
                <p class="mt-1 text-sm leading-relaxed text-base-content/75">
                  A private copy of this lab's machines, independent of any
                  team meeting. Nothing starts until you choose to.
                </p>
                <button
                  type="button"
                  class="btn btn-sm btn-primary min-h-11 mt-3 w-full"
                  disabled={!!busy || !!startBlocked}
                  aria-describedby={startBlocked ? "start-blocked" : undefined}
                  onclick={() => {
                    error = "";
                    startDialog.showModal();
                  }}><Play size={15} />Start cloud workspace</button
                >
                {#if startBlocked}<p
                    id="start-blocked"
                    class="mt-2 text-xs leading-relaxed text-base-content/75"
                  >
                    {startBlocked}
                    {#if otherRun}<a class="link" href={learnPath(otherRun.labID)}
                        >Go to that lab</a
                      >{/if}
                  </p>{/if}
              {/if}
            </div>
          </div>
          <div
            id="workspace-local"
            role="tabpanel"
            aria-labelledby="workspace-tab-local"
            hidden={mode !== "local"}
            class="mt-5 space-y-4 text-sm"
          >
            <p class="leading-relaxed">
              Download the pinned portable source with these instructions
              (<code>LAB-INSTRUCTIONS.md</code>). It runs through the standalone
              LabCentral CLI on your own configured AWS or Proxmox environment,
              not a laptop hypervisor. Cloud time does not apply, and you own
              cleanup.
            </p>
            {#if view.localAvailable}<a
                class="btn btn-sm min-h-11 w-full"
                href={endpoint(view.lab.id, "download")}
                download
                ><Download size={15} />Download lab (.tar.gz)</a
              >{:else}<p class="text-base-content/75">
                The portable download is unavailable for this lab right now.
              </p>{/if}
            <div>
              <div class="mb-1.5 flex items-center justify-between gap-2">
                <p class="font-medium">Run it with the CLI</p>
                <button
                  type="button"
                  class="btn btn-xs btn-ghost min-h-9"
                  onclick={copyCommands}><Copy size={13} />Copy</button
                >
              </div>
              <!-- Keyboard scrolling keeps long commands readable on phones. -->
              <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
              <pre
                class="max-w-full overflow-x-auto rounded-field border border-base-content/10 bg-base-200 p-3 text-xs leading-relaxed"
                tabindex="0"
                aria-label="CLI commands"><code>{commands}</code></pre>
              {#if copied}<p class="mt-1.5 text-xs" role="status">{copied}</p>{/if}
              <p class="mt-2 text-xs leading-relaxed text-base-content/75">
                Use your own environment file path. <code>labcentral inspect
                INSTANCE_ID</code> lists machine names for access.
              </p>
            </div>
            <p class="flex items-start gap-2 border-t border-base-content/10 pt-4 text-xs leading-relaxed text-base-content/75">
              <Lock size={13} class="mt-0.5 shrink-0" aria-hidden="true" />Answer
              stages still save progress here. Workspace checks need a hosted
              cloud workspace, so local runs cannot be verified automatically.
            </p>
          </div>
        </aside>
      </div>
    {/if}
  {:else if failure}
    <Notice message={failure} />
    <button class="btn btn-sm min-h-11" onclick={() => load()}>Retry</button>
  {:else}
    <div role="status" class="space-y-3">
      <span class="sr-only">Loading lab…</span>
      <div class="skeleton h-4 w-32"></div>
      <div class="skeleton h-7 w-80 max-w-full"></div>
      <div class="skeleton h-4 w-full max-w-xl"></div>
    </div>
  {/if}
</Guard>
<dialog bind:this={startDialog} class="modal" aria-labelledby="start-title">
  <div class="modal-box">
    <h2 id="start-title" class="text-lg font-semibold">
      Start a cloud workspace?
    </h2>
    {#if view}
      <p class="mt-3 font-medium">{view.lab.title}</p>
      <p class="mt-3 text-sm leading-relaxed">
        This allocates personal cloud machines for you. Time counts against
        your weekly allowance ({hours(view.quota.remainingSeconds)} left) from
        allocation until cleanup is confirmed. The run ends when your
        allowance runs out or the UTC week ends, whichever comes first, and its
        files are then deleted.
      </p>
    {/if}
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={!!busy}>Cancel</button>
      </form>
      <button
        class="btn btn-sm btn-primary min-h-11"
        disabled={!!busy}
        onclick={start}
        >{#if busy === "start"}<span class="loading loading-spinner loading-xs"
          ></span>{/if}Start workspace</button
      >
    </div>
  </div>
</dialog>
<dialog bind:this={stopDialog} class="modal" aria-labelledby="stop-title">
  <div class="modal-box">
    <h2 id="stop-title" class="text-lg font-semibold">
      Stop your cloud workspace?
    </h2>
    <p class="mt-3 text-sm leading-relaxed">
      This removes the workspace machines. Files in the workspace are deleted
      once the provider confirms cleanup, and quota stops counting then. Your
      stage progress is kept.
    </p>
    <Notice message={error} />
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm min-h-11" disabled={!!busy}>Cancel</button>
      </form>
      <button
        class="btn btn-sm btn-error min-h-11"
        disabled={!!busy}
        onclick={stop}
        >{#if busy === "stop"}<span class="loading loading-spinner loading-xs"
          ></span>{/if}Stop and delete files</button
      >
    </div>
  </div>
</dialog>
