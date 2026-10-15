import type { Overview, PracticeRun } from "./types";

// Private learning views belong to one user and CSRF session.
export function sessionScope(value: Overview | null) {
  return value ? `${value.user.id}:${value.csrf}` : "";
}

export function learnPath(id: string) {
  return `/learn/?id=${encodeURIComponent(id)}`;
}

export function endpoint(id: string, action = "") {
  return `/api/learning/${encodeURIComponent(id)}${action ? "/" + action : ""}`;
}

export function modeLabel(mode: string) {
  return mode === "instructor" ? "Instructor-led" : "Self-paced";
}

export function kindLabel(kind: string) {
  return kind === "action"
    ? "Checked automatically"
    : kind === "instructor"
      ? "Instructor"
      : "Answer";
}

// A run holds quota and cloud resources until the provider confirms cleanup.
export function liveRun(run: PracticeRun | null | undefined) {
  return run && run.status !== "ended" ? run : null;
}

export function transitional(run: PracticeRun | null | undefined) {
  return !!run && ["queued", "preparing", "ending"].includes(run.status);
}

export function runLabel(status: string) {
  return (
    (
      {
        queued: "Queued",
        preparing: "Preparing",
        ready: "Ready",
        failed: "Failed",
        ending: "Cleaning up",
        ended: "Ended",
      } as Record<string, string>
    )[status] ?? status
  );
}

export function hours(seconds: number) {
  const minutes = Math.max(0, Math.floor(seconds / 60));
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h ? (m ? `${h} h ${m} m` : `${h} h`) : `${m} m`;
}
