// In-memory only: the host retains no untrusted document content.
const drafts = new Set<string>();
export function featureDirty(key: string, dirty: boolean) {
  dirty ? drafts.add(key) : drafts.delete(key);
}
export function leaveFeatureDrafts() {
  if (!drafts.size) return true;
  if (!window.confirm("Discard unsaved template changes?")) return false;
  drafts.clear();
  return true;
}
export const hasFeatureDrafts = () => drafts.size > 0;
