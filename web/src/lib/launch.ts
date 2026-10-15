import { api, APIError } from "./api";
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
    // Another open of the same desktop may still be in flight (a cold desktop takes
    // about half a minute). Keep the tab and retry while the server asks for it.
    const deadline = Date.now() + 120_000;
    let result: Launch;
    for (;;) {
      try {
        result = await api<Launch>(path, {});
        break;
      } catch (e) {
        if (!(e instanceof APIError) || !e.retryAfter || Date.now() > deadline || tab.closed)
          throw e;
        tab.document.body.textContent = "Still starting your desktop…";
        await new Promise((r) => setTimeout(r, Math.min(e.retryAfter, 10) * 1000));
      }
    }
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
