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
