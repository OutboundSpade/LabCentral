// @ts-check
// Lesson panel for practice desktops. The desktop client imports this module on
// the authenticated desktop origin. Meeting desktops render no practice run and
// are left untouched; any other failure leaves the DCV viewport working on its own.
// Lesson text is rendered with DOM text nodes only: no HTML is ever parsed.
//
// Desktop-origin cookies are shared by every desktop tab, so opening another lab
// can change which allocation the cookie names. Each request carries the run
// rendered into this window, and the panel pins that run and its lab: a different
// identity ends the private view instead of switching lessons under a draft.

/** @typedef {import('../src/lib/types').LearningView} LearningView */
/** @typedef {import('../src/lib/types').LearningStep} LearningStep */
/** @typedef {import('../src/lib/types').LearningProgress} LearningProgress */
/** @typedef {import('../src/lib/types').LearningCheck} LearningCheck */

const POLL = 5000;

// Mirrors templateName in src/lib/display.ts (this static module cannot import it):
// "ccdc-service-recovery · Baseline practice" → "CCDC service recovery · Baseline practice".
const ACRONYMS = new Set([
  "ad", "api", "aws", "ccdc", "cdc", "cpu", "csv", "ctf", "dhcp", "dns", "ftp", "gpo",
  "http", "https", "ids", "iis", "ip", "ips", "ir", "ldap", "nfs", "os", "pki", "rdp",
  "siem", "smb", "smtp", "snmp", "sql", "ssh", "ssl", "tcp", "tls", "udp", "url", "vlan",
  "vm", "vpn", "waf", "xss",
]);
/** @param {string} value */
function templateName(value) {
  const [name, ...context] = value.split(" · ");
  const words = name
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((word, i) => {
      const lower = word.toLowerCase();
      if (ACRONYMS.has(lower)) return lower.toUpperCase();
      return i === 0 ? word.charAt(0).toUpperCase() + word.slice(1) : word;
    });
  return [words.join(" "), ...context].join(" · ");
}
/** The practice allocation this desktop window was rendered for; empty for meetings. */
const RUN = document.body?.dataset.practiceRun ?? "";
const MISMATCH = "This desktop window no longer matches your cloud lab. Reload it from LabCentral to continue.";

class LessonError extends Error {
  /** @param {number} status @param {string} message */
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

/**
 * GETs when body is undefined, otherwise POSTs JSON with the lesson CSRF header.
 * Every request names this window's run so the backend rejects a cookie that now
 * belongs to another allocation.
 * @param {string} path
 * @param {unknown} [body]
 * @returns {Promise<any>}
 */
async function request(path, body) {
  /** @type {RequestInit} */
  const init =
    body === undefined
      ? {
          cache: "no-store",
          credentials: "same-origin",
          headers: { Accept: "application/json", "X-LabCentral-Practice-Run": RUN },
        }
      : {
          method: "POST",
          cache: "no-store",
          credentials: "same-origin",
          headers: {
            "Content-Type": "application/json",
            "X-LabCentral-Lesson": "1",
            "X-LabCentral-Practice-Run": RUN,
          },
          body: JSON.stringify(body),
        };
  let res;
  try {
    res = await fetch(path, init);
  } catch {
    throw new LessonError(0, "The lesson service could not be reached.");
  }
  const text = await res.text().catch(() => "");
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {}
  if (!res.ok) {
    const plain = text.trim();
    const message =
      data && typeof data.error === "string"
        ? data.error
        : plain && plain.length < 200 && !plain.startsWith("<")
          ? plain
          : "The lesson request failed.";
    throw new LessonError(res.status, message);
  }
  return data;
}

/**
 * @param {any} next
 * @returns {next is LearningView}
 */
function valid(next) {
  return !!next && !!next.lab && typeof next.lab.id === "string" && Array.isArray(next.steps) && !!next.progress;
}

/**
 * @param {string} tag
 * @param {string} [className]
 * @param {string} [text]
 */
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// ---- Markdown (subset, html:false) ----------------------------------------

/** @param {string} url */
function safeURL(url) {
  try {
    const parsed = new URL(url);
    return ["https:", "http:", "mailto:"].includes(parsed.protocol) ? parsed.href : "";
  } catch {
    return "";
  }
}

/**
 * Finds `[label](url)` starting at text[i] === '['.
 * @param {string} text
 * @param {number} i
 */
function linkAt(text, i) {
  let depth = 0;
  let j = i;
  for (; j < text.length; j++) {
    if (text[j] === "\\") j++;
    else if (text[j] === "[") depth++;
    else if (text[j] === "]" && --depth === 0) break;
  }
  if (j >= text.length || text[j + 1] !== "(") return null;
  let k = j + 2;
  let parens = 0;
  for (; k < text.length; k++) {
    if (text[k] === "(") parens++;
    else if (text[k] === ")" && parens-- === 0) break;
  }
  if (k >= text.length) return null;
  const target = text.slice(j + 2, k).trim().split(/\s+/)[0] ?? "";
  return { label: text.slice(i + 1, j), url: target.replace(/^<|>$/g, ""), end: k + 1 };
}

/**
 * @param {string} href
 * @param {Node} content
 */
function anchor(href, content) {
  const a = /** @type {HTMLAnchorElement} */ (el("a"));
  a.href = href;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  a.append(content);
  return a;
}

/**
 * Inline code, emphasis, links and escapes. Unsafe or relative links stay text.
 * @param {string} text
 * @param {boolean} [links]
 * @returns {DocumentFragment}
 */
function inline(text, links = true) {
  const out = document.createDocumentFragment();
  let buf = "";
  const flush = () => {
    if (buf) out.append(buf);
    buf = "";
  };
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === "\\" && /[!-/:-@[-`{-~]/.test(text[i + 1] ?? "")) {
      buf += text[i + 1];
      i += 2;
      continue;
    }
    if (c === "`") {
      let n = 1;
      while (text[i + n] === "`") n++;
      const fence = "`".repeat(n);
      const end = text.indexOf(fence, i + n);
      if (end >= 0) {
        flush();
        out.append(el("code", "", text.slice(i + n, end).replace(/\n/g, " ")));
        i = end + n;
      } else {
        buf += fence;
        i += n;
      }
      continue;
    }
    if ((c === "[" || (c === "!" && text[i + 1] === "[")) && links) {
      const image = c === "!";
      const m = linkAt(text, image ? i + 1 : i);
      if (m) {
        flush();
        const href = image ? "" : safeURL(m.url);
        // Images are not loaded; their alt text is shown instead.
        out.append(href ? anchor(href, inline(m.label, false)) : inline(m.label, false));
        i = m.end;
        continue;
      }
    }
    if (c === "<" && links) {
      const m = text.slice(i).match(/^<((?:https?:\/\/|mailto:)[^\s<>]+)>/i);
      const href = m ? safeURL(m[1]) : "";
      if (m && href) {
        flush();
        out.append(anchor(href, document.createTextNode(m[1])));
        i += m[0].length;
        continue;
      }
    }
    if (c === "*" || c === "_") {
      const mark = text[i + 1] === c ? c + c : c;
      const from = i + mark.length;
      const opens =
        text[from] !== undefined &&
        !/\s/.test(text[from]) &&
        !(c === "_" && /\w/.test(text[i - 1] ?? ""));
      let close = -1;
      for (let k = from + 1; opens && k < text.length; k++) {
        if (
          text.startsWith(mark, k) &&
          !/\s/.test(text[k - 1]) &&
          !(c === "_" && /\w/.test(text[k + mark.length] ?? "")) &&
          (mark.length === 2 || text[k + 1] !== c)
        ) {
          close = k;
          break;
        }
      }
      if (close > 0) {
        flush();
        const node = el(mark.length === 2 ? "strong" : "em");
        node.append(inline(text.slice(from, close), links));
        out.append(node);
        i = close + mark.length;
        continue;
      }
    }
    buf += c;
    i++;
  }
  flush();
  return out;
}

const LIST = /^ {0,3}([-*+]|\d{1,9}[.)])\s+(.*)$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}> ?/;

/** @param {string} line */
function startsBlock(line) {
  return FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line) || LIST.test(line);
}

/**
 * Block Markdown: headings, paragraphs, lists, quotes, rules and code.
 * @param {string} source
 * @returns {DocumentFragment}
 */
function markdown(source) {
  const out = document.createDocumentFragment();
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = line.match(FENCE);
    if (fence) {
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence[1])) body.push(lines[i++]);
      i++;
      const pre = el("pre");
      pre.append(el("code", "", body.join("\n")));
      out.append(pre);
      continue;
    }
    if (/^( {4}|\t)/.test(line)) {
      const body = [];
      while (i < lines.length && (/^( {4}|\t)/.test(lines[i]) || !lines[i].trim())) {
        body.push(lines[i++].replace(/^( {4}|\t)/, ""));
      }
      while (body.length && !body[body.length - 1].trim()) body.pop();
      const pre = el("pre");
      pre.append(el("code", "", body.join("\n")));
      out.append(pre);
      continue;
    }
    const heading = line.match(HEADING);
    if (heading) {
      // The stage title is the panel's h3; lesson headings sit below it.
      const node = el(`h${Math.min(heading[1].length + 3, 6)}`);
      node.append(inline(heading[2]));
      out.append(node);
      i++;
      continue;
    }
    if (RULE.test(line)) {
      out.append(el("hr"));
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && QUOTE.test(lines[i])) body.push(lines[i++].replace(QUOTE, ""));
      const quote = el("blockquote");
      quote.append(markdown(body.join("\n")));
      out.append(quote);
      continue;
    }
    const first = line.match(LIST);
    if (first) {
      const ordered = /\d/.test(first[1]);
      const list = el(ordered ? "ol" : "ul");
      if (ordered && parseInt(first[1], 10) !== 1) list.setAttribute("start", String(parseInt(first[1], 10)));
      while (i < lines.length) {
        const m = lines[i].match(LIST);
        if (!m || /\d/.test(m[1]) !== ordered) break;
        const indent = lines[i].length - m[2].length;
        const body = [m[2]];
        i++;
        while (i < lines.length) {
          const next = lines[i];
          const lead = (next.match(/^ */) ?? [""])[0].length;
          if (next.trim() && lead >= Math.min(indent, 4)) body.push(next.slice(Math.min(lead, indent)));
          else if (next.trim() && !startsBlock(next) && body[body.length - 1].trim()) body.push(next);
          else if (!next.trim() && i + 1 < lines.length && /^ {2,}\S/.test(lines[i + 1])) body.push("");
          else break;
          i++;
        }
        const item = el("li");
        const content = markdown(body.join("\n"));
        // Keep tight lists tight: a single paragraph is rendered inline.
        if (content.childNodes.length === 1 && content.firstChild instanceof HTMLParagraphElement) {
          item.append(...content.firstChild.childNodes);
        } else item.append(content);
        list.append(item);
        while (i < lines.length && !lines[i].trim() && LIST.test(lines[i + 1] ?? "")) i++;
      }
      out.append(list);
      continue;
    }
    const body = [];
    while (i < lines.length && lines[i].trim() && (body.length === 0 || !startsBlock(lines[i]))) {
      body.push(lines[i++].trim());
    }
    const p = el("p");
    p.append(inline(body.join("\n")));
    out.append(p);
  }
  return out;
}

// ---- Panel -----------------------------------------------------------------

const PROSE =
  "prose prose-invert prose-sm max-w-none break-words prose-headings:font-semibold prose-headings:tracking-tight prose-h4:text-base prose-h5:text-sm prose-h6:text-sm prose-a:text-primary prose-pre:whitespace-pre-wrap prose-pre:bg-base-200 prose-pre:border prose-pre:border-base-content/10 prose-code:before:content-none prose-code:after:content-none";

/** @param {string} kind */
function kindLabel(kind) {
  return kind === "action" ? "Checked automatically" : kind === "instructor" ? "Instructor" : "Answer";
}

/** @param {string} value */
function time(value) {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

/**
 * Builds the panel once; later renders only update text, visibility and the
 * instructions when the stage changes, so drafts and focus survive polling.
 * Without an initial view the panel mounts already ended, showing `closed`.
 * @param {LearningView | null} initial
 * @param {HTMLElement} viewport
 * @param {string} [closed]
 */
function mount(initial, viewport, closed = "") {
  /** @type {LearningView | null} */
  let view = initial;
  // The lab and run this window opened with. Polls and writes never move the
  // panel to another identity; the student reloads instead.
  const labID = initial?.lab.id ?? "";
  let seq = 0;
  let busy = false;
  let ended = false;
  let stale = "";
  let refreshed = Date.now();
  let shownStep = "";
  let seenCompleted = -1;
  let instructionsKey = "";
  let outlineKey = "";
  let wasFinished = false;
  /** @type {Set<string>} */
  const openStages = new Set();
  let timer = 0;

  const panel = el(
    "aside",
    "flex shrink-0 flex-col border-b border-base-content/10 bg-base-100 font-sans text-sm leading-normal text-base-content lg:fixed lg:inset-y-0 lg:right-0 lg:z-10 lg:w-80 lg:border-b-0 lg:border-l xl:w-[22rem]",
  );
  panel.id = "lesson-panel";
  panel.setAttribute("aria-labelledby", "lesson-title");

  const top = el("div", "flex items-center gap-3 px-4 py-2 lg:py-3.5");
  const heading = el("div", "min-w-0 flex-1");
  const eyebrow = el("p", "truncate text-xs text-base-content/70");
  const title = el("h2", "truncate text-sm font-semibold leading-snug lg:text-base");
  title.id = "lesson-title";
  const count = el("p", "text-xs tabular-nums text-base-content/75");
  heading.append(eyebrow, title, count);
  const toggle = /** @type {HTMLInputElement} */ (el("input", "btn btn-sm btn-outline min-h-11 shrink-0 lg:hidden"));
  toggle.type = "button";
  toggle.value = "Hide";
  toggle.setAttribute("aria-label", "Hide lesson");
  toggle.setAttribute("aria-expanded", "true");
  toggle.setAttribute("aria-controls", "lesson-body");
  top.append(heading, toggle);

  const meter = /** @type {HTMLProgressElement} */ (el("progress", "progress progress-primary block h-1 w-full rounded-none"));
  meter.setAttribute("aria-label", "Stages complete");

  const body = el("div", "min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-4 pb-6 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary max-lg:max-h-[34dvh]");
  body.id = "lesson-body";
  // Focusable so keyboard users can scroll, and so copy shortcuts in lesson text
  // target the panel instead of the remote desktop.
  body.tabIndex = 0;
  body.setAttribute("role", "region");
  body.setAttribute("aria-label", "Lesson instructions");

  const notice = el("p", "alert alert-soft alert-warning mb-4 py-2 text-xs");
  notice.setAttribute("role", "status");
  notice.hidden = true;

  const stage = el("div");
  const stageMeta = el("p", "text-xs font-medium tracking-wide text-base-content/75 uppercase");
  const stageTitle = el("h3", "mt-1 text-lg font-semibold leading-snug tracking-tight");
  const instructions = el("div", `mt-3 ${PROSE}`);
  stage.append(stageMeta, stageTitle, instructions);

  const task = el("section", "mt-5 border-l-2 border-primary/60 py-1 pl-4");
  task.setAttribute("aria-label", "Stage task");

  // Answer stages.
  const answerForm = /** @type {HTMLFormElement} */ (el("form"));
  const answerPrompt = el("p", "mb-3 leading-relaxed whitespace-pre-line");
  answerPrompt.id = "lesson-answer-prompt";
  const answerLabel = /** @type {HTMLLabelElement} */ (el("label", "label mb-1.5 text-sm", "Your answer"));
  answerLabel.htmlFor = "lesson-answer";
  const answerRow = el("div", "flex flex-wrap gap-2");
  const answer = /** @type {HTMLInputElement} */ (el("input", "input input-sm min-h-11 min-w-0 flex-1 basis-40"));
  answer.id = "lesson-answer";
  answer.maxLength = 1000;
  answer.autocomplete = "off";
  answer.spellcheck = false;
  answer.required = true;
  const answerSubmit = /** @type {HTMLInputElement} */ (el("input", "btn btn-sm btn-primary min-h-11"));
  answerSubmit.type = "submit";
  answerSubmit.value = "Check answer";
  answerRow.append(answer, answerSubmit);
  answerForm.append(
    answerPrompt,
    answerLabel,
    answerRow,
    el("p", "mt-2 text-xs text-base-content/75", "A correct answer completes this stage and saves your progress."),
  );

  // Workspace check stages.
  const action = el("div");
  const actionPrompt = el("p", "mb-3 leading-relaxed whitespace-pre-line");
  const actionHelp = el(
    "p",
    "text-xs leading-relaxed text-base-content/75",
    "Checked every 20 seconds.",
  );
  const checkState = el("div", "mt-3");
  checkState.setAttribute("role", "status");
  const checkNow = /** @type {HTMLInputElement} */ (el("input", "btn btn-sm btn-outline min-h-11 mt-2"));
  checkNow.type = "button";
  checkNow.value = "Check now";
  action.append(actionPrompt, actionHelp, checkState, checkNow);

  // Instructor checkpoints.
  const checkpoint = el("div");
  const checkpointPrompt = el("p", "mt-2 leading-relaxed whitespace-pre-line");
  const complete = /** @type {HTMLInputElement} */ (el("input", "btn btn-sm btn-primary min-h-11 mt-3"));
  complete.type = "button";
  complete.value = "Complete checkpoint";
  checkpoint.append(
    el("p", "font-medium", "Instructor"),
    checkpointPrompt,
    el(
      "p",
      "mt-2 leading-relaxed text-base-content/75",
      "Your instructor has opened this checkpoint. Complete it when you have finished this part with your group.",
    ),
    complete,
  );

  const waiting = el("div");
  const waitingText = el("p", "mt-1 leading-relaxed text-base-content/75");
  waiting.append(el("p", "font-medium", "Waiting for your instructor"), waitingText);

  const done = el("div", "alert alert-soft alert-success text-sm");

  const success = el("p", "mt-3 text-sm text-success");
  success.setAttribute("role", "status");
  const failure = el("p", "mt-3 text-sm break-words text-error");
  failure.setAttribute("role", "alert");
  task.append(answerForm, action, checkpoint, waiting, done, success, failure);

  const outline = /** @type {HTMLDetailsElement} */ (el("details", "mt-6 border-t border-base-content/10 pt-2"));
  const outlineSummary = el("summary", "min-h-11 cursor-pointer rounded-field px-1 py-2.5 text-sm font-medium hover:bg-base-200");
  const outlineList = el("ol", "mt-2 min-w-0");
  outline.append(outlineSummary, outlineList);

  const endedText = el("p", "leading-relaxed");
  endedText.hidden = true;

  body.append(notice, endedText, stage, task, outline);
  panel.append(top, meter, body);

  // DCV listens for keyboard and clipboard events on the document. Keep events
  // from lesson controls and lesson text in the panel; the desktop keeps the rest.
  /** @param {EventTarget | null} target */
  const editable = (target) =>
    target instanceof HTMLElement &&
    (target.isContentEditable || target instanceof HTMLTextAreaElement || (target instanceof HTMLInputElement && !["button", "submit"].includes(target.type)));
  for (const type of ["keydown", "keyup", "keypress"]) panel.addEventListener(type, (event) => event.stopPropagation());
  for (const type of ["paste", "cut"]) panel.addEventListener(type, (event) => editable(event.target) && event.stopPropagation());
  panel.addEventListener("copy", (event) => event.stopPropagation());

  toggle.addEventListener("click", () => {
    const hide = !body.classList.contains("max-lg:hidden");
    body.classList.toggle("max-lg:hidden", hide);
    toggle.value = hide ? "Show" : "Hide";
    toggle.setAttribute("aria-label", hide ? "Show lesson" : "Hide lesson");
    toggle.setAttribute("aria-expanded", String(!hide));
  });

  /** @param {string} message @param {boolean} ok */
  function say(message, ok) {
    success.textContent = ok ? message : "";
    failure.textContent = ok ? "" : message;
  }

  /** @param {LessonError} e */
  function fail(e) {
    if (e.status === 401 || e.status === 403) {
      end(e.message);
      return true;
    }
    return false;
  }

  // Lost authorization clears every private detail and disables writes.
  /** @param {string} message */
  function end(message) {
    ended = true;
    view = null;
    seq++;
    clearTimeout(timer);
    answer.value = "";
    for (const node of [stageMeta, stageTitle, instructions, answerPrompt, actionPrompt, checkpointPrompt, checkState, waitingText, done, outlineSummary, outlineList]) {
      node.replaceChildren();
    }
    instructionsKey = outlineKey = "";
    openStages.clear();
    meter.removeAttribute("aria-valuetext");
    say("", true);
    render();
    endedText.textContent = `${message || "Lesson access ended."} Your saved progress is on your lesson page in LabCentral.`;
  }

  /** @param {LearningView} next */
  const matches = (next) => next.lab.id === labID && next.run?.id === RUN;

  async function load() {
    if (ended) return;
    const n = ++seq;
    try {
      const next = await request("/lesson");
      if (n !== seq || ended) return;
      if (!valid(next)) throw new LessonError(0, "The lesson response was not understood.");
      if (!matches(next)) {
        end(MISMATCH);
        return;
      }
      view = next;
      stale = "";
      refreshed = Date.now();
    } catch (e) {
      if (n !== seq || ended) return;
      const err = e instanceof LessonError ? e : new LessonError(0, "The lesson could not be refreshed.");
      if (fail(err)) return;
      stale = err.status === 404 ? "This lesson is no longer available." : err.message;
    }
    render();
  }

  function poll() {
    clearTimeout(timer);
    if (ended) return;
    timer = window.setTimeout(async () => {
      if (!document.hidden) await load();
      poll();
    }, POLL);
  }
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && !ended) void load().then(poll);
  });

  /**
   * @param {string} path
   * @param {unknown} payload
   * @returns {Promise<any>}
   */
  async function submit(path, payload) {
    if (busy || ended) return null;
    busy = true;
    say("", true);
    render();
    try {
      const result = await request(path, payload);
      // A poll that started before this write must not overwrite its result.
      seq++;
      return result;
    } catch (e) {
      const err = e instanceof LessonError ? e : new LessonError(0, "The lesson request failed.");
      if (!fail(err)) say(err.message, false);
      return null;
    } finally {
      busy = false;
      if (!ended) {
        render();
        void load();
      }
    }
  }

  answerForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    const step = view && current(view).step;
    if (!view || !step || step.kind !== "answer") return;
    const before = view.progress.completedSteps;
    /** @type {LearningProgress | null} */
    const progress = await submit("/lesson/answer", { stepID: step.id, answer: answer.value });
    // An ended view stays cleared, and a response for another lab is ignored.
    if (ended || !view || !progress || typeof progress.completedSteps !== "number" || progress.labID !== labID) return;
    // A poll that finished while this write was in flight may already be newer.
    const latest = Math.max(view.progress.completedSteps, progress.completedSteps);
    view.progress = { ...progress, completedSteps: latest };
    if (progress.completedSteps > before) {
      answer.value = "";
      render();
      say(progress.lastFeedback || "Answer accepted.", true);
    } else if (latest === before) {
      render();
      say(progress.lastFeedback || "That answer did not match. Review the lab and try again.", false);
    } else render();
  });

  /** @param {"action" | "instructor"} kind */
  async function check(kind) {
    if (!view) return;
    const { step, completed } = current(view);
    if (!step || step.kind !== kind) return;
    /** @type {LearningCheck | null} */
    const result = await submit("/lesson/check", { stepID: step.id });
    if (ended || !view || !result || result.stepID !== step.id || result.labID !== labID) return;
    // Keep a newer polled check for this stage (for example running or passed).
    const known = view.check;
    if (!known || known.stepID !== result.stepID || !(Date.parse(known.updated) > Date.parse(result.updated))) view.check = result;
    if (kind === "instructor" && result.status === "passed") {
      view.progress = { ...view.progress, completedSteps: Math.max(view.progress.completedSteps, completed + 1) };
      render();
      say("Checkpoint complete.", true);
    } else render();
  }
  // Blocked controls stay focusable (aria-disabled) so a pending check or
  // request never drops keyboard focus out of the panel into the desktop.
  /** @param {HTMLInputElement} control */
  const blocked = (control) => busy || control.getAttribute("aria-disabled") === "true";
  /**
   * @param {HTMLInputElement} control
   * @param {string} reason
   */
  function block(control, reason) {
    control.setAttribute("aria-disabled", String(!!reason));
    control.classList.toggle("btn-disabled", !!reason);
    control.title = reason;
  }
  checkNow.addEventListener("click", () => {
    if (!blocked(checkNow)) void check("action");
  });
  complete.addEventListener("click", () => {
    if (!blocked(complete)) void check("instructor");
  });

  /** @param {LearningView} v */
  function current(v) {
    const total = v.steps.length;
    const completed = Math.min(Math.max(v.progress.completedSteps, 0), total);
    const released = v.lab.mode === "instructor" ? Math.min(v.releasedSteps, total) : total;
    const finished = total > 0 && completed >= total;
    return { total, completed, released, finished, step: finished ? undefined : v.steps[completed] };
  }

  /**
   * @param {LearningView} v
   * @param {number} completed
   * @param {number} released
   */
  function renderOutline(v, completed, released) {
    const key = JSON.stringify([v.steps.map((s) => [s.id, s.title, s.kind, s.instructions]), completed, released]);
    if (key === outlineKey) return;
    outlineKey = key;
    outlineList.replaceChildren(
      ...v.steps.map((step, index) => {
        const state = index < completed ? "Complete" : index >= released ? "Not released" : index === completed ? "Current stage" : "Upcoming";
        const item = el("li", "relative min-w-0 pl-9");
        if (index < v.steps.length - 1) {
          const line = el("span", `absolute top-8 bottom-0 left-[0.6875rem] w-px ${index < completed ? "bg-primary/45" : "bg-base-content/15"}`);
          line.setAttribute("aria-hidden", "true");
          item.append(line);
        }
        const dot = el(
          "span",
          `absolute top-2.5 left-0 grid size-6 place-items-center rounded-full border text-xs font-medium tabular-nums ${
            state === "Current stage"
              ? "border-primary bg-primary text-primary-content"
              : state === "Complete"
                ? "border-base-content/25 bg-base-300 text-base-content"
                : "border-base-content/20 bg-base-100 text-base-content/70"
          }`,
          String(index + 1),
        );
        dot.setAttribute("aria-hidden", "true");
        item.append(dot);
        const name = el("span", "block leading-snug");
        name.append(el("span", "sr-only", `Stage ${index + 1}: `), index < released ? step.title : `Stage ${index + 1}`);
        const meta = el("span", "mt-0.5 block text-xs text-base-content/70", `${kindLabel(step.kind)} · ${state}`);
        const box = el("div", "mb-1 px-2.5 py-2");
        if (index === completed && index < released) {
          item.setAttribute("aria-current", "step");
          name.classList.add("font-medium");
          box.append(name, meta);
          item.append(box);
        } else if (index < released) {
          // Earlier and read-ahead stages open in place; their task stays here.
          const details = /** @type {HTMLDetailsElement} */ (el("details", "mb-1"));
          const summary = el("summary", "min-h-11 cursor-pointer rounded-field px-2.5 py-2 hover:bg-base-200");
          summary.append(name, meta);
          const text = el("div", `px-2.5 pt-1 pb-3 ${PROSE}`);
          details.append(summary, text);
          const fill = () => {
            if (details.open) {
              openStages.add(step.id);
              if (!text.childNodes.length) text.append(markdown(step.instructions));
            } else openStages.delete(step.id);
          };
          details.addEventListener("toggle", fill);
          details.open = openStages.has(step.id);
          if (details.open) fill();
          item.append(details);
        } else {
          name.classList.add("text-base-content/60");
          box.append(name, meta);
          item.append(box);
        }
        return item;
      }),
    );
  }

  /** @param {LearningCheck | null} c */
  function renderCheck(c) {
    checkState.replaceChildren();
    if (!c) return;
    const line = el("p", "flex items-center gap-2");
    if (c.status === "queued" || c.status === "running") {
      const spin = el("span", "loading loading-spinner loading-xs");
      spin.setAttribute("aria-hidden", "true");
      line.append(spin, c.status === "queued" ? "Check queued." : "Checking your workspace…");
    } else if (c.status === "failed") {
      line.className = "font-medium text-error";
      line.textContent = "Not passing yet. Keep working; the next automatic check runs shortly.";
    } else if (c.status === "unavailable") {
      line.className = "font-medium text-warning";
      line.textContent = "The check could not run. This is not a failed result; it will be tried again.";
    } else if (c.status === "passed") {
      line.className = "font-medium";
      line.textContent = "Check passed.";
    } else return;
    checkState.append(line);
    if (c.report) checkState.append(el("p", "mt-1.5 break-words whitespace-pre-line text-base-content/80", c.report));
    const at = time(c.updated);
    if (at) checkState.append(el("p", "mt-1 text-xs text-base-content/75", `Updated ${at}`));
  }

  function render() {
    const v = view;
    stage.hidden = task.hidden = outline.hidden = meter.hidden = !v;
    endedText.hidden = !ended;
    for (const control of [answer, answerSubmit, checkNow, complete]) control.disabled = ended;
    answerSubmit.setAttribute("aria-busy", String(busy));
    if (!v) {
      eyebrow.textContent = "Lesson";
      title.textContent = ended ? "Lesson unavailable" : "Lesson";
      count.textContent = "";
      notice.hidden = true;
      return;
    }
    const { total, completed, released, finished, step } = current(v);
    if (seenCompleted >= 0 && completed > seenCompleted) {
      // Progress advanced, here or by a guest check or another tab.
      answer.value = "";
      if (!success.textContent) say(finished ? "Lab complete." : `Stage ${completed} complete. Your progress is saved.`, true);
    }
    seenCompleted = completed;
    const waitingRelease = !finished && completed >= released;
    const stepID = step?.id ?? "";
    if (stepID !== shownStep) {
      if (shownStep) answer.value = "";
      failure.textContent = "";
      shownStep = stepID;
    }

    eyebrow.textContent = `${v.lab.mode === "instructor" ? "Instructor-led" : "Self-paced"} lesson`;
    title.textContent = v.lab.attempt ? templateName(v.lab.title) : v.lab.title;
    count.textContent = finished
      ? `All ${total} stages complete`
      : `Stage ${completed + 1} of ${total} · ${completed} complete`;
    meter.max = Math.max(total, 1);
    meter.value = completed;
    meter.setAttribute("aria-valuetext", `${completed} of ${total} stages complete`);
    outlineSummary.textContent = `All stages · ${completed} of ${total} complete`;
    if (finished && !wasFinished) outline.open = true;
    wasFinished = finished;

    notice.hidden = !stale;
    notice.textContent = stale
      ? `Showing progress from ${time(new Date(refreshed).toISOString())}. ${stale} Retrying every few seconds.`
      : "";

    stage.hidden = finished;
    if (step) {
      stageMeta.textContent = `Stage ${completed + 1} of ${total} · ${kindLabel(step.kind)}`;
      stageTitle.textContent = waitingRelease ? `Stage ${completed + 1}` : step.title;
      const key = waitingRelease ? "" : `${step.id}\n${step.instructions}`;
      if (key !== instructionsKey) {
        instructionsKey = key;
        instructions.replaceChildren(key ? markdown(step.instructions) : "");
      }
      instructions.hidden = waitingRelease;
    }

    answerForm.hidden = !step || waitingRelease || step.kind !== "answer";
    action.hidden = !step || waitingRelease || step.kind !== "action";
    checkpoint.hidden = !step || waitingRelease || step.kind !== "instructor";
    waiting.hidden = !waitingRelease;
    done.hidden = !finished;
    task.classList.toggle("border-primary/60", !finished);
    task.classList.toggle("border-base-content/15", finished);
    if (finished) done.textContent = `Lab complete. All ${total} stages are done; earlier stages stay readable below.`;
    if (waitingRelease) {
      waitingText.textContent = `Your instructor releases stages in order. This panel checks for stage ${completed + 1} every few seconds; your progress is saved.`;
    }
    if (step) {
      answerPrompt.textContent = actionPrompt.textContent = checkpointPrompt.textContent = step.prompt;
      answerPrompt.hidden = actionPrompt.hidden = checkpointPrompt.hidden = !step.prompt;
      if (step.prompt) answer.setAttribute("aria-describedby", answerPrompt.id);
      else answer.removeAttribute("aria-describedby");
    }
    const c = v.check && step && v.check.stepID === step.id ? v.check : null;
    const pending = !!c && (c.status === "queued" || c.status === "running");
    const ready = v.run?.status === "ready" && v.run.labID === v.lab.id;
    block(checkNow, pending ? "A workspace check is already running." : ready ? "" : "Your workspace is not ready.");
    block(complete, "");
    if (step?.kind === "action") renderCheck(c);
    else checkState.replaceChildren();
    renderOutline(v, completed, released);
  }

  // Practice lessons return to their lesson page on the application origin.
  const back = document.querySelector("body > header > a");
  if (initial && back instanceof HTMLAnchorElement) {
    try {
      back.href = new URL(`/learn/?id=${encodeURIComponent(initial.lab.id)}`, back.href).href;
      back.textContent = "← Lesson page";
      back.classList.add("underline");
    } catch {}
  }
  restyleDialog();
  // Keep the panel narrow on laptops so the remote desktop keeps most of the width.
  document.body.classList.add("lg:pr-80", "xl:pr-[22rem]");
  viewport.before(panel);
  if (!initial) {
    end(closed);
    return;
  }
  render();
  poll();
}

// The shared stylesheet's reset also applies to the legacy paste dialog; restore
// its spacing with utilities so the fallback paste flow stays readable.
function restyleDialog() {
  const dialog = document.getElementById("paste-dialog");
  if (!dialog) return;
  dialog.classList.add("m-auto", "w-[min(24rem,calc(100vw-2rem))]", "rounded-box", "border", "border-base-content/15", "bg-base-200", "p-5", "font-sans", "text-sm", "text-base-content", "backdrop:bg-base-300/70");
  dialog.querySelector("h2")?.classList.add("mb-3", "text-base", "font-semibold");
  for (const p of dialog.querySelectorAll("p")) p.classList.add("mb-3");
  dialog.querySelector("textarea")?.classList.add("textarea", "mb-3", "w-full");
}

function stylesheet() {
  return new Promise((resolve, reject) => {
    const link = /** @type {HTMLLinkElement} */ (document.createElement("link"));
    link.rel = "stylesheet";
    link.href = "/lesson-ui.css";
    link.onload = () => resolve(link);
    link.onerror = () => {
      link.remove();
      reject(new Error("Lesson styles are unavailable."));
    };
    document.head.append(link);
  });
}

/** @param {number} attempt */
async function start(attempt) {
  const viewport = document.getElementById("desktop-viewport");
  if (!viewport || document.getElementById("lesson-panel")) return;
  const retry = () => window.setTimeout(() => void start(attempt + 1), Math.min(60000, POLL * 2 ** attempt));
  /** @type {LearningView | null} */
  let view = null;
  // Why this window cannot show its lesson; the panel then mounts without one.
  let closed = "";
  try {
    view = await request("/lesson");
  } catch (e) {
    const status = e instanceof LessonError ? e.status : 0;
    // 404: no lesson here. 401: desktop access itself ended, as DCV reports.
    if (status === 404 || status === 401) return;
    if (status !== 403) {
      retry();
      return;
    }
    closed = e instanceof LessonError ? e.message : "";
  }
  if (!closed) {
    if (!valid(view)) {
      retry();
      return;
    }
    if (view.run?.id !== RUN) {
      view = null;
      closed = MISMATCH;
    }
  }
  try {
    await stylesheet();
  } catch {
    retry();
    return;
  }
  mount(closed ? null : view, viewport, closed);
}

// Meeting desktops render no practice run; leave them exactly as they were.
if (RUN) void start(0).catch(() => {});
