import { api } from "./api";
import type { Launch } from "./types";

// Call directly from a click handler: the tab is reserved before any await,
// because provisioning can outlast the browser's user activation.
export async function launchDesktop(path: string, waiting: string) {
  const tab = window.open("about:blank", "_blank");
  if (!tab)
    throw new Error(
      "Allow a new tab for LabCentral, then open your desktop again.",
    );
  tab.opener = null;
  tab.document.title = "Starting your desktop · LabCentral";
  tab.document.body.textContent = waiting;
  try {
    const result = await api<Launch>(path, {});
    if (tab.closed)
      throw new Error("Desktop tab closed. Open it again when you are ready.");
    const form = tab.document.createElement("form");
    form.method = "POST";
    form.action = result.url;
    const input = tab.document.createElement("input");
    input.type = "hidden";
    input.name = "ticket";
    input.value = result.ticket;
    form.append(input);
    tab.document.body.replaceChildren(form);
    form.submit();
  } catch (e) {
    tab.close();
    throw e;
  }
}
