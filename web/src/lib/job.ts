import type { AdminView, Job, Template } from "./types";

export function jobName(kind: string) {
  return (
    (
      {
        prepare: "Prepare labs",
        build: "Build images",
        "remove-images": "Remove images",
        "prune-images": "Remove unused images",
        refresh: "Refresh templates",
        exercise: "Run exercise",
        check: "Service checks",
        "access-cleanup": "Desktop cleanup",
        end: "End meeting",
        "practice-prepare": "Prepare practice workspace",
        "practice-end": "Clean up practice workspace",
        "learning-check": "Verify lab stage",
      } as Record<string, string>
    )[kind] ?? "Lab operation"
  );
}

// The build or image-removal job that describes a revision's images.
export function revisionJob(data: AdminView | null | undefined, id: string) {
  return (
    data?.templates.find((t) => t.id === id)?.operation ??
    data?.jobs.find(
      (j) => j.target === id && (j.kind === "build" || j.kind === "remove-images"),
    )
  );
}

export function jobActive(job: Job | null | undefined) {
  return job?.status === "queued" || job?.status === "running";
}

// One status for a revision's images, shared by cards and the revision panel.
export function revisionState(t: Template, job: Job | null | undefined) {
  if (jobActive(job))
    return {
      status: job!.status,
      label:
        job!.status === "queued"
          ? "Queued"
          : job!.kind === "build"
            ? "Building images"
            : "Removing images",
    };
  if (job?.status === "failed")
    return { status: "failed", label: job.kind === "build" ? "Build failed" : "Cleanup failed" };
  if (t.removing) return { status: "queued", label: "Cleanup pending" };
  if (t.retired) return { status: "ended", label: "Retired" };
  if (t.built) return { status: "ready", label: "Ready" };
  if (t.removedAt) return { status: "ended", label: "Images removed" };
  return { status: "pending", label: "Not built" };
}

// A revision can be built when it is current-format and has no images in use.
export function buildable(t: Template) {
  return !t.built && !t.removing && !t.retired;
}

export function buildLabel(t: Template, job: Job | null | undefined) {
  return job?.kind === "build" && job.status === "failed"
    ? "Retry build"
    : t.removedAt
      ? "Rebuild images"
      : "Build images";
}
