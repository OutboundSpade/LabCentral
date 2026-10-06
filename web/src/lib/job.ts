export function jobName(kind: string) {
  return (
    (
      {
        prepare: "Prepare labs",
        build: "Build images",
        "remove-images": "Remove images",
        refresh: "Refresh templates",
        exercise: "Run exercise",
        check: "Service checks",
        "access-cleanup": "Desktop cleanup",
        end: "End meeting",
      } as Record<string, string>
    )[kind] ?? "Lab operation"
  );
}
