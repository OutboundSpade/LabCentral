<script lang="ts">
  import PageHeader from "$lib/PageHeader.svelte";
  import { templateName } from "$lib/display";
  import { onMount, untrack } from "svelte";
  import { browser } from "$app/environment";
  import { page } from "$app/state";
  import {
    ArrowLeft,
    ArrowRight,
    BookOpen,
    Check,
    CircleCheck,
    Copy,
    Download,
    ExternalLink,
    Hourglass,
    Laptop,
    Play,
    RotateCcw,
    Send,
    Shuffle,
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
  import AttemptButton from "$lib/AttemptButton.svelte";
  import { session, api, APIError, refresh, date } from "$lib/api";
  import { launchDesktop } from "$lib/launch";
  import { render, prose } from "$lib/markdown";
  import {
    sessionScope,
    learnPath,
    endpoint,
    kindLabel,
    liveRun,
    transitional,
    runLabel,
    hours,
  } from "$lib/learning";
  import {
    templatePath,
    attemptBlocked,
    difficultyLabel,
    minutes,
    breakRange,
    type AttemptView,
  } from "$lib/library";
  import type {
    LearningSolution,
    LearningProgress,
    LearningCheck,
    PracticeRun,
  } from "$lib/types";

  let mounted = $state(false);
  // Query parameters are unavailable while prerendering; read them after hydration.
  const id = $derived(
    browser && mounted ? (page.url.searchParams.get("id") ?? "") : "",
  );
  let view = $state<AttemptView | null>(null);
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
      const next = await api<AttemptView>(endpoint(lab));
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
  let localDialog: HTMLDialogElement;
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
        view.machines?.some((m) => m.os === "windows" && m.access.includes("desktop"))
          ? "Starting your Windows desktop and its lesson panel. This can take a few minutes…"
          : "Starting your practice desktop and its lesson panel…",
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
  const scenario = $derived(view?.scenario ?? null);
  const randomized = $derived(!!scenario || !!view?.lab.scenarioID);
  const startBlocked = $derived(
    !view
      ? ""
      : !view.cloudReady
        ? scenario
          ? "Workspaces aren't available for this scenario yet."
          : "Workspaces aren't available for this lab yet. You can run it locally."
        : otherRun
          ? "Another lab's workspace is still running."
          : view.quota.remainingSeconds < 60
            ? `Out of cloud time until ${date(view.quota.resetsAt, { weekday: "short", hour: "numeric", minute: "2-digit" })}.`
            : "",
  );
  const verifyBlocked = $derived(
    checkPending
      ? "A workspace check is already running."
      : !ownRun
        ? "Start a workspace to verify this stage."
        : ownRun.status !== "ready"
          ? "Available when your workspace is ready."
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
      Open a template from the library to start practice, or continue one of
      your attempts.
      {#snippet actions()}<a class="btn btn-sm btn-primary min-h-11" href="/labs/"
          >Browse templates<ArrowRight size={15} /></a
        ><a class="btn btn-sm min-h-11" href="/labs/?view=attempts">My attempts</a>{/snippet}
    </EmptyState>
  {:else if missing}<EmptyState icon={BookOpen} title="This lab is unavailable">
      It may no longer be offered. Your saved progress is kept; any active
      workspace can still be stopped from the library.
      {#snippet actions()}<a class="btn btn-sm min-h-11" href="/labs/"
          ><ArrowLeft size={15} />Back to library</a
        >{/snippet}
    </EmptyState>
  {:else if view}
    <PageHeader title={view.lab.attempt ? templateName(view.lab.title) : view.lab.title} description={view.lab.description}>
      {#snippet eyebrow()}<a class="link link-hover" href={view?.lab.attempt ? "/labs/?view=attempts" : "/labs/"}>{view?.lab.attempt ? "My attempts" : "Practice"}</a>{#if view?.lab.attempt}{" · "}<span>{randomized ? "Randomized scenario" : "Baseline practice"} attempt</span>{/if}{/snippet}
      <div class="flex w-full items-center gap-3 sm:w-56">
        <progress
          class="progress progress-primary h-1.5 min-w-24 flex-1"
          value={completed}
          max={steps.length || 1}
          aria-label="Stages complete"
        ></progress>
        <span class="text-xs tabular-nums text-base-content/70" role="status"
          >{completed} of {steps.length} stages complete</span
        >
      </div>
      {#if view.lab.attempt}<a class="btn btn-sm min-h-11 lg:min-h-8" href={templatePath(view.lab.template)}>Template revision</a>{/if}
    </PageHeader>
    {#if scenario}<section
        aria-labelledby="scenario-title"
        class="rounded-box border border-base-content/9 bg-base-200/70 mb-4 px-4 py-3 text-sm"
      >
        <p class="flex items-center gap-2 text-xs font-medium text-base-content/70">
          <Shuffle size={13} aria-hidden="true" />Randomized scenario
        </p>
        <h2 id="scenario-title" class="mt-1 font-semibold">{scenario.title}</h2>
        {#if scenario.description}<p class="mt-1 max-w-[68ch] leading-relaxed text-base-content/80">
            {scenario.description}
          </p>{/if}
        <p class="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-base-content/70">
          {#if scenario.difficulty}<span>{difficultyLabel(scenario.difficulty)}</span>{/if}
          {#if scenario.estimatedMinutes}<span>{minutes(scenario.estimatedMinutes)}</span>{/if}
          <span>{breakRange(scenario)}</span>
        </p>
        <p class="mt-2 max-w-[68ch] text-xs leading-relaxed text-base-content/70">
          Investigate the workspace to find what changed.
        </p>
      </section>{/if}
    <section
      aria-labelledby="workspace-title"
      class="rounded-box border border-base-content/9 bg-base-200/70 mb-8 flex flex-wrap items-center gap-x-5 gap-y-3 px-4 py-3"
    >
      <h2 id="workspace-title" class="sr-only">Workspace</h2>
      {#if ownRun}
        <div class="min-w-0 flex-1 basis-64 text-sm">
          <p class="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span class="font-medium">Workspace</span>
            <span aria-live="polite"
              ><Status status={ownRun.status} label={runLabel(ownRun.status)} /></span
            >
          </p>
          {#if ownRun.status === "queued" || ownRun.status === "preparing"}
            <p class="mt-0.5 text-base-content/70">
              Your machines are being prepared. Keep reading while you wait.
            </p>
          {:else if ownRun.status === "ready"}
            <div class="mt-1">
              <Deadline
                expires={ownRun.expires}
                warning="Workspace files are deleted when this cloud run ends. Copy anything you need now."
                due="This cloud run has reached its limit. Cleanup is due."
              />
            </div>
          {:else if ownRun.status === "failed"}
            <p class="mt-0.5 break-words text-error">
              {ownRun.error || "The workspace could not be prepared."}
              {#if randomized}Stop it to clean up, then start a new scenario attempt
                from the <a class="link" href={templatePath(view.lab.template)}>template revision</a
                >.{:else}Stop it, then start again.{/if}
            </p>
          {:else if ownRun.status === "ending"}
            <p class="mt-0.5 text-base-content/70">
              Cleaning up. Quota stops once removal is confirmed.
            </p>
          {/if}
        </div>
        <div class="flex flex-wrap gap-2">
          {#if ownRun.status === "ready"}<button
              type="button"
              class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
              disabled={!!busy}
              aria-busy={busy === "access"}
              title="Opens in a new tab with these instructions beside the desktop"
              onclick={openDesktop}
              >{#if busy === "access"}<span
                  class="loading loading-spinner loading-xs"
                ></span>{:else}<ExternalLink size={15} />{/if}Open desktop</button
            >{/if}
          {#if ownRun.status !== "ending"}<button
              type="button"
              class="btn btn-sm btn-error btn-soft min-h-11 lg:min-h-8"
              disabled={!!busy}
              onclick={() => {
                error = "";
                stopDialog.showModal();
              }}><Square size={14} />Stop workspace</button
            >{/if}
        </div>
      {:else}
        <div class="min-w-0 flex-1 basis-64 text-sm">
          <p class="font-medium">Workspace</p>
          <p class="mt-0.5 text-base-content/70" id="start-blocked">
            {#if startBlocked}{startBlocked}
              {#if otherRun}<a class="link" href={learnPath(otherRun.labID)}>Go to that lab</a>{/if}
            {:else if randomized}Scenarios run only in a private workspace.
            {:else}Start a private cloud copy of this lab's machines.{/if}
          </p>
        </div>
        <div class="w-40 shrink-0"><Quota quota={view.quota} /></div>
        <div class="flex flex-wrap gap-2">
          <button
            type="button"
            class="btn btn-sm btn-primary min-h-11 lg:min-h-8"
            disabled={!!busy || !!startBlocked}
            aria-describedby="start-blocked"
            onclick={() => {
              error = "";
              startDialog.showModal();
            }}><Play size={15} />Start workspace</button
          >
          {#if !randomized}<button
              type="button"
              class="btn btn-sm min-h-11 lg:min-h-8"
              onclick={() => localDialog.showModal()}
              ><Laptop size={15} />Run locally</button
            >{/if}
        </div>
      {/if}
    </section>
    <Notice message={error} dismissible />
    {#if failure}<p class="mb-4 text-sm text-warning" role="status">
        Live progress updates are unavailable ({failure}). Showing your last
        loaded progress.
      </p>{/if}
    {#if !steps.length}
      <p class="text-sm">This lab has no stages yet.</p>
    {:else}
      <div class="grid items-start gap-8 lg:grid-cols-[14rem_minmax(0,1fr)] lg:gap-12">
        <nav aria-label="Lab stages" class="hidden lg:sticky lg:top-20 lg:block">
          <StageRail
            {steps}
            {completed}
            {released}
            selected={shown}
            onselect={choose}
          />
        </nav>
        <section class="min-w-0" aria-labelledby="stage-title">
          <details class="collapse collapse-arrow rounded-box border border-base-content/9 bg-base-200/70 mb-6 lg:hidden">
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
              class="alert alert-soft alert-success mb-3 text-sm"
              role="status"
            >
              <CircleCheck size={18} aria-hidden="true" /><span
                >Lab complete. All {steps.length} stages are done.</span
              >
            </div>
            {#if view.lab.attempt}<div class="mb-6 flex flex-wrap items-center gap-2">
                <AttemptButton
                  template={view.lab.template}
                  scenarioID={view.lab.scenarioID ?? ""}
                  label={randomized ? "New scenario" : "Try again"}
                  name={randomized
                    ? `New scenario: ${scenario?.title ?? view.lab.title}`
                    : `Try again: ${templateName(view.lab.title)}`}
                  icon={randomized ? Shuffle : RotateCcw}
                  primary
                  blocked={attemptBlocked(view.run)}
                  describedby="repeat-help"
                />
                <a class="btn btn-sm min-h-11 lg:min-h-8" href={templatePath(view.lab.template)}
                  >Template revision<ArrowRight size={15} /></a
                >
                {#if attemptBlocked(view.run)}<p id="repeat-help" class="basis-full text-xs text-base-content/70">
                    {attemptBlocked(view.run)}
                  </p>{/if}
              </div>{:else}<div class="mb-6"></div>{/if}{/if}
          {#if step}
            <p class="text-xs font-medium text-base-content/70">
              Stage {shown + 1} of {steps.length}
            </p>
            <h2 id="stage-title" class="mt-1 text-xl font-semibold tracking-tight">
              {stepReleased ? step.title : `Stage ${shown + 1}`}
            </h2>
            {#if !stepReleased}
              <div class="rounded-box border border-base-content/9 bg-base-200/70 mt-5 flex items-start gap-3 p-4 text-sm">
                <Hourglass size={17} class="mt-0.5 shrink-0 text-base-content/70" aria-hidden="true" />
                <div>
                  <p class="font-medium">Waiting for your instructor</p>
                  <p class="mt-1 text-base-content/70">
                    This stage opens automatically when it's released.
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
                  "rounded-box border border-base-content/9 bg-base-200/70 mt-8 p-5",
                  isCurrent ? "border-primary/40" : "",
                ]}
              >
                {#if feedback}<p
                    class={[
                      "mb-4 flex items-start gap-2 text-sm",
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
                {#if shown < completed}
                  <p class="flex items-center gap-2 text-sm text-base-content/80">
                    <CircleCheck size={16} class="text-success" aria-hidden="true" />Stage complete
                  </p>
                {:else if !isCurrent}
                  <p class="text-sm text-base-content/80">
                    Finish stage {completed + 1} to unlock this one.
                  </p>
                  <button
                    type="button"
                    class="btn btn-sm min-h-11 mt-2 -ml-3"
                    onclick={() => choose(completed)}
                    ><ArrowLeft size={15} />Go to stage {completed + 1}</button
                  >
                {:else if step.kind === "answer"}
                  <form onsubmit={submitAnswer}>
                    {#if step.prompt}<p
                        id="answer-prompt"
                        class="mb-3 text-sm font-medium leading-relaxed whitespace-pre-line"
                      >
                        {step.prompt}
                      </p>{/if}
                    <label class="sr-only" for="stage-answer">Your answer</label>
                    <div class="flex flex-wrap gap-2">
                      <input
                        id="stage-answer"
                        class="input input-sm min-h-11 min-w-0 flex-1 basis-56"
                        bind:value={answer}
                        maxlength="1000"
                        autocomplete="off"
                        placeholder="Your answer"
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
                  </form>
                {:else if step.kind === "action"}
                  {#if step.prompt}<p
                      class="mb-4 text-sm font-medium leading-relaxed whitespace-pre-line"
                    >
                      {step.prompt}
                    </p>{/if}
                  <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
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
                    <p id="verify-help" class="text-xs text-base-content/70">
                      {verifyBlocked || "Also checked automatically while this stage is open."}
                    </p>
                  </div>
                  {#if check}<div class="mt-4 text-sm" role="status">
                      {#if check.status === "queued" || check.status === "running"}
                        <p class="flex items-center gap-2">
                          <span class="loading loading-spinner loading-xs"></span>
                          {check.status === "queued"
                            ? "Check queued."
                            : "Checking your workspace…"}
                        </p>
                      {:else if check.status === "failed"}
                        <p class="font-medium text-error">Not passing yet.</p>
                      {:else if check.status === "unavailable"}
                        <p class="font-medium text-warning">
                          The check couldn't reach your workspace. Try again shortly.
                        </p>
                      {:else if check.status === "passed"}
                        <p class="font-medium">Check passed.</p>
                      {/if}
                      {#if check.report}<p
                          class="mt-1.5 break-words whitespace-pre-line text-base-content/80"
                        >
                          {check.report}
                        </p>{/if}
                      <p class="mt-1 text-xs text-base-content/60">
                        {date(check.updated)}
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
                  <button
                    type="button"
                    class="btn btn-sm btn-primary min-h-11 mt-4"
                    disabled={!!busy}
                    onclick={verify}
                    >{#if busy === "check"}<span
                        class="loading loading-spinner loading-xs"
                      ></span>{:else}<Check size={15} />{/if}Complete checkpoint</button
                  >
                {/if}
              </section>
              {#if step.hasSolution && shown <= completed}
                <section class="mt-4" aria-label="Stage solution">
                  {#if solution}<div class={[prose, "rounded-box border border-base-content/9 bg-base-200/70 p-5"]}>{@html render(solution)}</div><button class="btn btn-sm min-h-11 mt-2 -ml-3" onclick={() => solution = ""}>Hide solution</button>
                  {:else}<button type="button" class="btn btn-sm min-h-11 -ml-3 text-base-content/70" disabled={solutionBusy} onclick={revealSolution}>{#if solutionBusy}<span class="loading loading-spinner loading-xs"></span>{:else}<BookOpen size={15} />{/if}View solution</button>{/if}
                  {#if solutionError}<p class="mt-2 text-sm text-error" role="alert">{solutionError}</p>{/if}
                </section>
              {/if}
            {/if}
            <div
              class="mt-10 flex flex-wrap items-center justify-between gap-2 border-t border-base-content/8 pt-4"
            >
              <button
                type="button"
                class="btn btn-sm min-h-11"
                disabled={shown === 0}
                onclick={() => choose(shown - 1)}
                ><ArrowLeft size={15} />Previous stage</button
              >
              <button
                type="button"
                class="btn btn-sm min-h-11"
                disabled={shown >= steps.length - 1 || shown + 1 >= released}
                onclick={() => choose(shown + 1)}
                >Next stage<ArrowRight size={15} /></button
              >
            </div>
          {/if}
        </section>
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
      Start a workspace?
    </h2>
    {#if view}
      <p class="mt-3 font-medium">{view.lab.title}</p>
      <p class="mt-3 text-sm leading-relaxed text-base-content/80">
        Uses your weekly cloud time ({hours(view.quota.remainingSeconds)} left)
        until you stop it. It ends automatically when your time runs out or the
        week resets, and its files are deleted.
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
      Stop your workspace?
    </h2>
    <p class="mt-3 text-sm leading-relaxed text-base-content/80">
      Workspace files are deleted. Your stage progress is kept.
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
<dialog bind:this={localDialog} class="modal" aria-labelledby="local-title">
  <div class="modal-box max-w-xl">
    <h2 id="local-title" class="text-lg font-semibold">Run locally</h2>
    <p class="mt-2 text-sm leading-relaxed text-base-content/80">
      Download the lab and run it with the LabCentral CLI on your own AWS or
      Proxmox environment. Answers still save here; workspace checks need the
      workspace.
    </p>
    {#if view?.localAvailable && !randomized}<a
        class="btn btn-sm btn-primary min-h-11 mt-4"
        href={endpoint(view.lab.id, "download")}
        download
        ><Download size={15} />Download lab (.tar.gz)</a
      >{:else}<p class="mt-4 text-sm text-base-content/70">
        The download is unavailable for this lab right now.
      </p>{/if}
    <div class="mt-5">
      <div class="mb-1.5 flex items-center justify-between gap-2">
        <p class="text-sm font-medium">Commands</p>
        <button
          type="button"
          class="btn btn-sm min-h-9"
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
    </div>
    <div class="modal-action">
      <form method="dialog"><button class="btn btn-sm min-h-11">Close</button></form>
    </div>
  </div>
  <form method="dialog" class="modal-backdrop"><button>Close</button></form>
</dialog>
