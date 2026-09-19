import { connect } from "./sdk.js";
export const bridge = await connect();
export const context = await bridge.request("context.read");
export const app = document.querySelector("#app");
app.replaceChildren();
export function el(tag, text = "", attrs = {}) {
  const node = document.createElement(tag);
  node.textContent = text;
  for (const [key, value] of Object.entries(attrs))
    if (value !== undefined) node.setAttribute(key, String(value));
  return node;
}
export const status = el("p", "", { role: "status", class: "status" });
app.append(status);
export function message(text, error = false) {
  status.textContent = text;
  status.classList.toggle("error", error);
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
  const b = el("button", text, { type: "button", class: kind });
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
  const wrap = el("label", label);
  const input = el(tag, "", { "aria-label": label, ...attrs });
  wrap.append(input);
  parent.append(wrap);
  return input;
}
export function check(parent, label, checked = false) {
  const wrap = el("label", "", { class: "check" });
  const input = el("input", "", { type: "checkbox" });
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
export function confirm(title, description, items = []) {
  return new Promise((resolve) => {
    const d = el("dialog");
    d.append(el("h2", title), el("p", description));
    const list = el("ul");
    items.forEach((i) => list.append(el("li", i)));
    d.append(list);
    const done = (value) => {
      d.close();
      d.remove();
      resolve(value);
    };
    const actions = el("div", "", { class: "actions" });
    actions.append(
      button("Cancel", () => done(false)),
      button("Confirm", () => done(true), "danger"),
    );
    d.append(actions);
    d.oncancel = (e) => {
      e.preventDefault();
      done(false);
    };
    document.body.append(d);
    d.showModal();
  });
}
export function labCard(lab, parent, { end, onchange } = {}) {
  const card = el("article");
  card.append(
    button(lab.name ?? lab.id, () =>
      request("navigation.lab", { instanceId: lab.id }),
    ),
    el("p", `${lab.phase} · ${lab.ownerKind}: ${lab.ownerId}`),
  );
  if (lab.expiresAt)
    card.append(
      el("small", `Expires ${new Date(lab.expiresAt).toLocaleString()}`),
    );
  if (end && lab.phase !== "destroyed")
    card.append(
      button(
        "End lab",
        async () => {
          if (
            await confirm(
              "End this lab?",
              "This deletes the lab and its files.",
              [lab.id],
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
  parent.append(card);
  return card;
}

export async function initialize(fn) {
  for (const stop of polls) stop();
  app.replaceChildren(status);
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
