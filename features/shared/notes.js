/** Render a deliberately small, inert subset of student Markdown. Never use HTML. */
/** @param {HTMLElement} node @param {string} text */
export function renderNotes(node, text) {
  node.replaceChildren();
  const lines = (text || "").split("\n");
  /** @param {HTMLElement} parent @param {string} value */
  const inline = (parent, value) => {
    value.split(/(`[^`]+`)/g).forEach((part) => {
      const child = document.createElement(part.startsWith("`") ? "code" : "span");
      child.textContent = part.startsWith("`") ? part.slice(1, -1) : part;
      if (child.tagName === "CODE") child.className = "rounded bg-base-200 px-1 font-mono text-xs";
      parent.append(child);
    });
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    const image = /^!\[([^\]]*)\]\((\/student-guides\/[a-z0-9-]+\.svg)\)$/.exec(line);
    if (image) {
      const a = document.createElement("a"); a.href = image[2]; a.target = "_blank"; a.rel = "noopener noreferrer";
      const img = document.createElement("img"); img.src = image[2]; img.alt = image[1]; img.className = "my-4 w-full rounded-xl"; a.append(img); node.append(a); continue;
    }
    if (line.startsWith("|") && /^\|[\s:|\-]+\|$/.test((lines[i + 1] || "").trim())) {
      const wrap = document.createElement("div"); wrap.className = "my-4 overflow-x-auto";
      const table = document.createElement("table"); table.className = "table table-sm w-full";
      /** @param {string} value @param {boolean} heading */
      const row = (value, heading) => { const tr = document.createElement("tr"); for (const cell of value.trim().slice(1, -1).split("|")) { const td = document.createElement(heading ? "th" : "td"); inline(td, cell.trim()); tr.append(td); } table.append(tr); };
      row(line, true); i++;
      while (i + 1 < lines.length && lines[i + 1].trim().startsWith("|")) row(lines[++i], false);
      wrap.append(table); node.append(wrap); continue;
    }
    const heading = /^(#{1,3})\s+(.+)$/.exec(line);
    const p = document.createElement(heading ? "h3" : "p");
    p.className = heading ? "mt-5 mb-2 font-semibold text-base" : "my-2 text-sm leading-relaxed break-words";
    inline(p, heading ? heading[2] : line); node.append(p);
  }
}
