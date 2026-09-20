// Restrict focus to the active feature frame while its native dialog is open.
// Restore exactly the prior inert state on close, navigation or channel failure.
export function isolateFeatureModal(frame: HTMLElement): () => void {
  const changed: HTMLElement[] = [];
  let branch: HTMLElement | null = frame;
  while (branch?.parentElement) {
    for (const sibling of branch.parentElement.children) {
      if (
        sibling !== branch &&
        sibling instanceof HTMLElement &&
        !sibling.inert
      ) {
        sibling.inert = true;
        changed.push(sibling);
      }
    }
    branch = branch.parentElement;
    if (branch === document.body) break;
  }
  return () =>
    changed.forEach((node) => {
      node.inert = false;
    });
}
