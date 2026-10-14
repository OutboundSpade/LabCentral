import { derived, get } from "svelte/store";
import { api, APIError, session, loading, loadError } from "./api";
import { sessionScope } from "./learning";
import type {
  CreateTemplateAttemptRequest,
  LearningCatalog,
  LearningLab,
  LearningView,
  PracticeRun,
  PublicTemplate,
  TemplateScenarioSummary,
} from "./types";

export type LibraryTemplate = PublicTemplate;
export type ScenarioSummary = TemplateScenarioSummary;
export type AttemptLab = LearningLab;
export type AttemptView = LearningView;
export type AttemptCatalog = LearningCatalog;

export function templatePath(id: string) {
  return `/labs/?template=${encodeURIComponent(id)}`;
}

export function scenarios(t: LibraryTemplate | null | undefined) {
  return t?.scenarios ?? [];
}

// Cloud practice uses the template’s pinned desktop machine.
export function desktopWorkstation(t: LibraryTemplate) {
  return t.machines.some(
    (m) => m.name === t.desktopMachine && m.access.includes("desktop"),
  );
}

export const runBlocked =
  "Stop your active cloud workspace and let cleanup finish before starting another attempt.";

// A new attempt is refused while any personal cloud run is still live.
export function attemptBlocked(run: PracticeRun | null | undefined) {
  return run && run.status !== "ended" ? runBlocked : "";
}

// Baseline practice uses the revision's self-paced stages; coach-only revisions
// and revisions without eligible stages cannot start one.
export function baselineBlocked(t: LibraryTemplate) {
  if (t.coachOnly) return "Coach-only revisions are previews for instructors.";
  if (!t.practiceStageIDs?.length)
    return "This revision has no self-paced stages.";
  return "";
}

export function baselineHint(t: LibraryTemplate) {
  if (!desktopWorkstation(t))
    return "Cloud workspaces need a desktop workstation, which this revision doesn't define. You can still run it locally.";
  if (!t.imagesBuilt)
    return "Cloud workspaces need built images. You can still run it locally.";
  return "Run it in a cloud workspace or on your own infrastructure.";
}

// Randomized scenarios run only in a cloud workspace.
export function scenarioBlocked(t: LibraryTemplate) {
  if (t.coachOnly) return "Coach-only revisions are previews for instructors.";
  if (!desktopWorkstation(t))
    return "Scenarios need a cloud workspace with a desktop workstation, which this revision doesn't define.";
  if (!t.imagesBuilt)
    return "Scenarios need a cloud workspace, and this revision's images aren't built yet.";
  return "";
}

export function difficultyLabel(value: string) {
  return value ? value[0].toUpperCase() + value.slice(1) : "";
}

export function minutes(value: number) {
  if (!value) return "";
  const h = Math.floor(value / 60);
  const m = value % 60;
  return h ? (m ? `${h} h ${m} min` : `${h} h`) : `${m} min`;
}

export function breakRange(s: ScenarioSummary) {
  const issues = (n: number) => `${n} ${n === 1 ? "issue" : "issues"}`;
  return s.minBreaks === s.maxBreaks
    ? issues(s.maxBreaks)
    : `${s.minBreaks}–${issues(s.maxBreaks)}`;
}

export function attempts(catalog: AttemptCatalog | null) {
  return (catalog?.labs ?? [])
    .filter((lab) => lab.attempt === true)
    .toSorted((a, b) => b.publishedAt.localeCompare(a.publishedAt));
}

export function legacy(catalog: AttemptCatalog | null) {
  return (catalog?.labs ?? []).filter((lab) => lab.attempt !== true);
}

// An attempt request is identified by identity, exact template revision and mode.
// Its random request ID is kept until the server confirms an outcome, so a retry
// after an unknown result (including a page reload) replays the same request
// instead of creating another attempt. Only the owner's user ID, the template
// and scenario IDs and the random request IDs are stored for this browser tab.
const ID_BYTES = 12; // 24 lowercase hexadecimal characters.
const STORAGE = "labcentral.attemptRequests";
const ID = /^[0-9a-f]{24}$/;
const pending = new Map<string, string>();
const inflight = new Map<string, Promise<AttemptResult>>();
let owner = "";
let generation = 0;

function storage() {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

function save() {
  try {
    const store = storage();
    if (!store) return;
    if (!owner || !pending.size) store.removeItem(STORAGE);
    else
      store.setItem(
        STORAGE,
        JSON.stringify({ owner, requests: Object.fromEntries(pending) }),
      );
  } catch {
    // Storage can be disabled or full; the in-memory request IDs still apply.
  }
}

function restore(user: string) {
  try {
    const saved = JSON.parse(storage()?.getItem(STORAGE) ?? "null");
    if (saved?.owner !== user || typeof saved.requests !== "object") return;
    for (const [k, id] of Object.entries(saved.requests ?? {}))
      if (typeof id === "string" && ID.test(id)) pending.set(k, id);
  } catch {
    // Unreadable entries are ignored; they never hold lesson content.
  }
}

function reset(user: string) {
  owner = user;
  generation++;
  pending.clear();
  inflight.clear();
}

// Call after the server confirms sign-out: unconfirmed request IDs must not be
// restored by a later sign-in, even for the same user.
export function clearAttemptRequests() {
  reset("");
  save();
}

// Wait for a confirmed identity: hydration and transient refresh failures keep
// pending IDs, while sign-out or a different user discards them.
derived([session, loading, loadError], (values) => values).subscribe(
  ([value, isLoading, failure]) => {
    if (isLoading) return;
    const user = value?.user.id ?? "";
    if (!value && failure) return;
    if (user === owner) {
      // A confirmed unauthenticated load discards entries left by an earlier session.
      if (!user) save();
      return;
    }
    const first = !owner && !!user;
    reset(user);
    if (first) restore(user);
    save();
  },
);

function key(template: string, scenarioID: string) {
  return JSON.stringify([template, scenarioID]);
}

export type AttemptResult =
  | { ok: true; lab: AttemptLab; scope: string }
  | {
      ok: false;
      message: string;
      unknown: boolean;
      scope: string;
      status: number;
    };

export function attemptPending(template: string, scenarioID = "") {
  return pending.has(key(template, scenarioID));
}

export function createAttempt(
  template: string,
  scenarioID = "",
): Promise<AttemptResult> {
  const current = get(session);
  const scope = sessionScope(current);
  const user = current?.user.id ?? "";
  if (!user || user !== owner)
    return Promise.resolve({
      ok: false,
      message: "Sign in to start practice.",
      unknown: false,
      scope,
      status: 401,
    });
  const k = key(template, scenarioID);
  const running = inflight.get(k);
  if (running) return running;
  const gen = generation;
  let id = pending.get(k);
  if (!id) {
    const bytes = crypto.getRandomValues(new Uint8Array(ID_BYTES));
    id = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
    pending.set(k, id);
    save();
  }
  const body: CreateTemplateAttemptRequest = { requestID: id };
  if (scenarioID) body.scenarioID = scenarioID;
  // Only this request may settle its own entry; identity changes start a new generation.
  const settle = () => {
    if (gen === generation && pending.get(k) === id) {
      pending.delete(k);
      save();
    }
  };
  // Assigned before the request settles; the async body awaits before using it.
  let promise: Promise<AttemptResult> | undefined;
  promise = (async (): Promise<AttemptResult> => {
    try {
      const lab = await api<AttemptLab>(
        `/api/templates/${encodeURIComponent(template)}/attempt`,
        body,
      );
      settle();
      return { ok: true, lab, scope };
    } catch (e) {
      // A 4xx response is a confirmed rejection; anything else may have succeeded.
      const confirmed =
        e instanceof APIError &&
        e.status >= 400 &&
        e.status < 500 &&
        ![408, 429].includes(e.status);
      if (confirmed) settle();
      return {
        ok: false,
        message: confirmed
          ? (e as Error).message
          : "We couldn't confirm whether your attempt was created. Retry safely repeats the same request, or check My attempts.",
        unknown: !confirmed,
        scope,
        status: e instanceof APIError ? e.status : 0,
      };
    } finally {
      if (gen === generation && inflight.get(k) === promise) inflight.delete(k);
    }
  })();
  inflight.set(k, promise);
  return promise;
}
