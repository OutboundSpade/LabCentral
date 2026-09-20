import { connect } from "./sdk.js";
export const bridge = await connect();
export const context = await bridge.request("context.read");
export const app = document.querySelector("#app");
app.replaceChildren();
const styles = {
  p: "my-2 break-words",
  h2: "text-lg font-semibold mb-3 break-words",
  h3: "font-semibold break-words",
  section: "min-w-0 space-y-3 [&>.btn+.btn]:ml-2",
  article:
    "min-w-0 rounded-box border border-base-300 p-4 space-y-2 [&>.btn+.btn]:ml-2 [&>small+.btn]:mt-3 [&>.btn+small]:mt-3",
  details: "min-w-0 rounded-box border border-base-300 p-4 space-y-3",
  summary:
    "cursor-pointer font-medium focus-visible:outline-2 focus-visible:outline-primary",
  small: "block text-xs text-base-content/65",
  pre: "max-h-80 overflow-auto whitespace-pre-wrap text-xs font-mono [overflow-wrap:anywhere]",
  fieldset: "fieldset border border-base-300 rounded-box p-4",
  legend: "fieldset-legend",
  nav: "flex flex-wrap items-center gap-2",
  ul: "list-disc pl-5 space-y-1",
};
const aliases = {
  actions: "flex flex-wrap items-center gap-3 my-3",
  grid: "grid gap-3 md:grid-cols-2",
  muted: "text-base-content/65",
  error: "text-error",
  badge: "badge badge-sm",
  source: "font-mono min-h-80",
};
export function el(tag, text = "", attrs = {}) {
  const node = document.createElement(tag);
  node.textContent = text;
  node.className = styles[tag] ?? "";
  for (const [key, value] of Object.entries(attrs))
    if (value !== undefined) {
      if (key === "class")
        node.className += " " + (aliases[String(value)] ?? String(value));
      else node.setAttribute(key, String(value));
    }
  return node;
}
export const status = el("div", "", {
  role: "status",
  class:
    "fixed bottom-4 right-4 left-4 z-40 rounded-box bg-base-100 border border-base-300 p-4 shadow-lg",
  "data-feedback": "",
});
status.hidden = true;
document.body.append(status);
let messageTimer;
export function message(text, error = false) {
  clearTimeout(messageTimer);
  const target =
    document.querySelector("dialog[open] [data-feedback]") ?? status;
  target.replaceChildren();
  target.hidden = !text;
  target.setAttribute("role", error ? "alert" : "status");
  if (!text) return;
  target.append(
    el("p", text, { class: error ? "text-error" : "" }),
    button("Dismiss message", () => {
      target.hidden = true;
    }),
  );
  if (!error)
    messageTimer = setTimeout(() => {
      target.hidden = true;
    }, 8000);
}
export function tabs(names, parent = app) {
  const nav = el("div", "", {
    role: "tablist",
    "aria-label": "Page sections",
    class: "tabs tabs-border sticky top-0 z-10 bg-base-100 flex-wrap",
  });
  parent.append(nav);
  const panels = {},
    controls = {};
  const select = (name) => {
    message("");
    for (const n of names) {
      panels[n].hidden = n !== name;
      controls[n].setAttribute("aria-selected", String(n === name));
      controls[n].classList.toggle("tab-active", n === name);
      controls[n].tabIndex = n === name ? 0 : -1;
    }
    window.scrollTo({ top: 0 });
    parent.dispatchEvent(new CustomEvent("tabchange", { detail: name }));
  };
  for (const name of names) {
    const id = crypto.randomUUID(),
      control = el("button", name, {
        type: "button",
        role: "tab",
        class: "tab min-h-11",
        id,
        "aria-controls": id + "-panel",
      });
    const panel = el("div", "", {
      role: "tabpanel",
      id: id + "-panel",
      "aria-labelledby": id,
      class: "min-w-0 space-y-5 py-4",
    });
    controls[name] = control;
    panels[name] = panel;
    nav.append(control);
    parent.append(panel);
    control.onclick = () => select(name);
    control.onkeydown = (e) => {
      const i = names.indexOf(name);
      const next =
        e.key === "ArrowRight"
          ? (i + 1) % names.length
          : e.key === "ArrowLeft"
            ? (i + names.length - 1) % names.length
            : e.key === "Home"
              ? 0
              : e.key === "End"
                ? names.length - 1
                : -1;
      if (next >= 0) {
        e.preventDefault();
        select(names[next]);
        controls[names[next]].focus();
      }
    };
  }
  select(names[0]);
  return { panels, select };
}
export function modal(title, description = "") {
  const d = el("dialog", "", {
    class: "modal",
    "aria-labelledby": crypto.randomUUID(),
  });
  const box = el("div", "", { class: "modal-box w-11/12 max-w-3xl space-y-4" });
  box.append(el("h2", title, { id: d.getAttribute("aria-labelledby") }));
  if (description) box.append(el("p", description, { class: "muted" }));
  const feedback = el("div", "", { "data-feedback": "", role: "status" });
  feedback.hidden = true;
  box.append(feedback);
  d.append(box);
  document.body.append(d);
  let opener;
  d.addEventListener("close", () => opener?.isConnected && opener.focus());
  return {
    element: d,
    body: box,
    open() {
      opener = document.activeElement;
      d.showModal();
    },
    close() {
      d.close();
    },
  };
}
export function stableList(parent, items, render) {
  const old = new Map([...parent.children].map((n) => [n.dataset.key, n]));
  for (const item of items) {
    const key = item.id,
      signature = JSON.stringify(item);
    let node = old.get(key);
    old.delete(key);
    if (!node || node.dataset.signature !== signature) {
      const focused = node?.contains(document.activeElement),
        label = focused
          ? document.activeElement.getAttribute("aria-label") ||
            document.activeElement.textContent
          : null;
      const next = render(item);
      next.dataset.key = key;
      next.dataset.signature = signature;
      if (node) node.replaceWith(next);
      else parent.append(next);
      node = next;
      if (focused) {
        const control = [...node.querySelectorAll("button,input")].find(
          (n) => (n.getAttribute("aria-label") || n.textContent) === label,
        );
        control?.focus();
      }
    }
  }
  for (const node of old.values()) node.remove();
}
export function setDirty(dirty) {
  return request("page.dirty", { dirty });
}
export function navigate(destination) {
  return request("page.navigate", { destination });
}
export const can = (action) => context.allowedActions?.includes(action);
// Keep package bursts below the host's request/concurrency bounds, including
// simultaneous polling and a fast sequence of user actions.
let active = 0,
  nextStart = 0,
  queueTimer,
  closed = false;
const queue = [];
function drain() {
  clearTimeout(queueTimer);
  if (closed || active >= 3 || !queue.length) return;
  const wait = nextStart - performance.now();
  if (wait > 0) {
    queueTimer = setTimeout(drain, wait);
    return;
  }
  const work = queue.shift();
  active++;
  nextStart = performance.now() + 100;
  bridge
    .request(work.method, { input: work.input })
    .then(work.resolve, work.reject)
    .finally(() => {
      active--;
      drain();
    });
  drain();
}
export function request(method, input = {}) {
  if (closed || queue.length >= 64)
    return Promise.reject(new Error("Feature request queue unavailable"));
  return new Promise((resolve, reject) => {
    queue.push({ method, input, resolve, reject });
    drain();
  });
}
addEventListener(
  "pagehide",
  () => {
    closed = true;
    clearTimeout(queueTimer);
    for (const work of queue.splice(0))
      work.reject(new Error("Feature closed"));
    bridge.close();
  },
  { once: true },
);
// A failed response retains its command identity. An edited command receives a new key.
const retries = new Map();
export async function mutate(method, input = {}) {
  const identity = JSON.stringify([method, input]);
  const idempotencyKey = retries.get(identity) ?? crypto.randomUUID();
  retries.set(identity, idempotencyKey);
  const result = await request(method, { ...input, idempotencyKey });
  retries.delete(identity);
  return result;
}
export async function action(fn) {
  try {
    message("");
    return await fn();
  } catch (error) {
    message(error.message ?? "Request failed", true);
  }
}
export function button(text, fn, kind = "") {
  const b = el("button", text, {
    type: "button",
    class:
      kind === "primary"
        ? "btn btn-sm btn-primary min-h-11 h-auto py-2 whitespace-nowrap shrink-0"
        : kind === "danger"
          ? "btn btn-sm btn-outline text-error min-h-11 h-auto py-2 whitespace-nowrap shrink-0"
          : "btn btn-sm min-h-11 h-auto py-2 whitespace-nowrap shrink-0",
  });
  b.onclick = async () => {
    if (b.disabled) return;
    b.disabled = true;
    try {
      await action(fn);
    } finally {
      b.disabled = false;
    }
  };
  return b;
}
export function field(parent, label, tag = "input", attrs = {}) {
  const wrap = el("label", label, { class: "grid gap-2 my-3 min-w-0" });
  const input = el(tag, "", { "aria-label": label, ...attrs });
  input.className +=
    tag === "textarea"
      ? " textarea w-full min-w-0"
      : tag === "select"
        ? " select w-full min-w-0"
        : attrs.type === "file"
          ? " file-input w-full min-w-0"
          : " input w-full min-w-0";
  wrap.append(input);
  parent.append(wrap);
  return input;
}
export function check(parent, label, checked = false) {
  const wrap = el("label", "", {
    class: "flex items-center gap-3 my-2 min-w-0",
  });
  const input = el("input", "", {
    type: "checkbox",
    class: "checkbox checkbox-sm shrink-0",
  });
  input.checked = checked;
  wrap.append(input, document.createTextNode(label));
  parent.append(wrap);
  return input;
}
export function section(title, parent = app) {
  const s = el("section");
  s.append(el("h2", title));
  parent.append(s);
  return s;
}
export function details(title, parent = app) {
  const d = el("details");
  d.append(el("summary", title));
  parent.append(d);
  return d;
}
export function options(select, items, empty = "Select…") {
  const value = select.value;
  select.replaceChildren(
    el("option", empty, { value: "" }),
    ...items.map((i) =>
      el("option", i.label ?? i.name ?? i.id, { value: i.id }),
    ),
  );
  if (items.some((i) => i.id === value)) select.value = value;
}
export function inspect(parent, value) {
  const p = el("pre", JSON.stringify(value, null, 2));
  parent.append(p);
  return p;
}
export async function all(method, input = {}, cursorKey = "cursor") {
  const items = [];
  let cursor = "";
  const seen = new Set();
  do {
    const page = await request(method, {
      ...input,
      ...(cursor ? { [cursorKey]: cursor } : {}),
    });
    items.push(...(page.items ?? []));
    cursor = page.nextCursor ?? "";
    if (seen.has(cursor)) throw new Error("Repeated collection cursor");
    if (cursor) seen.add(cursor);
    if (items.length > 10000)
      throw new Error("Collection exceeds display limit");
  } while (cursor);
  return items;
}
const polls = new Set();
export function poll(fn, milliseconds = 5000) {
  let stopped = false,
    timer;
  const tick = async () => {
    try {
      await fn();
    } catch (error) {
      message(error.message, true);
    }
    if (!stopped) timer = setTimeout(tick, milliseconds);
  };
  timer = setTimeout(tick, milliseconds);
  addEventListener(
    "pagehide",
    () => {
      stopped = true;
      clearTimeout(timer);
    },
    { once: true },
  );
  const stop = () => {
    stopped = true;
    clearTimeout(timer);
    polls.delete(stop);
  };
  polls.add(stop);
  return stop;
}
export function confirm(
  title,
  description,
  items = [],
  actionLabel = "Confirm",
) {
  return new Promise((resolve) => {
    const d = modal(title, description),
      list = el("ul");
    items.forEach((i) => list.append(el("li", i)));
    d.body.append(list);
    let finished = false;
    const done = (value) => {
      if (finished) return;
      finished = true;
      d.close();
      d.element.remove();
      resolve(value);
    };
    const actions = el("div", "", { class: "actions" });
    actions.append(
      button("Cancel", () => done(false)),
      button(actionLabel, () => done(true), "danger"),
    );
    d.body.append(actions);
    d.element.oncancel = (e) => {
      e.preventDefault();
      done(false);
    };
    d.open();
  });
}
export function labCard(lab, parent, { end, onchange } = {}) {
  const card = el("article", "", {
      class:
        "flex flex-col min-[700px]:flex-row min-[700px]:items-center justify-between gap-6",
    }),
    summary = el("div", "", { class: "min-w-0 flex-1" }),
    actions = el("div", "", {
      class: "flex items-center flex-wrap gap-3 shrink-0",
    });
  const name = lab.name || "Untitled lab";
  summary.append(
    el("h3", name),
    el(
      "p",
      [
        lab.ownerName,
        lab.desiredLifecycle === "destroyed" && lab.phase !== "destroyed"
          ? "Ending…"
          : lab.phase,
      ]
        .filter(Boolean)
        .join(" · "),
    ),
  );
  if (lab.expiresAt)
    summary.append(
      el("small", `Expires ${new Date(lab.expiresAt).toLocaleString()}`),
    );
  const identity = document.createElement("details");
  identity.className = "mt-2 text-xs text-base-content/65";
  identity.append(
    el("summary", "Lab ID"),
    el("code", lab.id, { class: "break-all text-xs" }),
  );
  summary.append(identity);
  actions.append(
    button("Open lab", () => request("navigation.lab", { instanceId: lab.id })),
  );
  if (lab.desiredLifecycle !== "destroyed")
    for (const resource of (lab.resources ?? []).filter(
      (r) => (r.kind === "VM" || r.virtualMachine) && r.phase === "ready",
    ))
      actions.append(
        button(
          (lab.resources ?? []).filter(
            (r) => r.kind === "VM" || r.virtualMachine,
          ).length > 1
            ? `Console: ${resource.id.replace(/^[^.]+\./, "")}`
            : "Open console",
          () =>
            request("navigation.console", {
              instanceId: lab.id,
              resourceId: resource.id,
            }),
          "primary",
        ),
      );
  if (end && lab.phase !== "destroyed" && lab.desiredLifecycle !== "destroyed")
    actions.append(
      button(
        "End lab",
        async () => {
          if (
            await confirm(
              "End this lab?",
              "This deletes the lab and its files.",
              [name, lab.ownerName].filter(Boolean),
              "End lab",
            )
          ) {
            await mutate("labs.destroy", { instanceId: lab.id });
            message("Deletion queued.");
            await onchange?.();
          }
        },
        "danger",
      ),
    );
  card.append(summary, actions);
  parent.append(card);
  return card;
}

export async function initialize(fn) {
  for (const stop of polls) stop();
  app.replaceChildren();
  message("");
  const loading = el("p", "Loading feature…", { role: "status" });
  document.body.append(loading);
  app.hidden = true;
  try {
    await fn();
  } catch (error) {
    message(`This feature could not load: ${error.message}`, true);
    app.append(button("Retry loading", () => initialize(fn)));
  } finally {
    loading.remove();
    app.hidden = false;
  }
}
