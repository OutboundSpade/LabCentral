import MarkdownIt from "markdown-it";
import { renderMermaidSVG } from "beautiful-mermaid";

// Raw HTML stays disabled: lesson and handover text is rendered as Markdown only.
const markdown = new MarkdownIt({ html: false, linkify: false });
const fence = markdown.renderer.rules.fence!;
const diagrams = new Map<string, string>();

markdown.renderer.rules.fence = (tokens, index, options, env, renderer) => {
  const token = tokens[index];
  if (token.info.trim().toLowerCase() !== "mermaid") {
    return fence(tokens, index, options, env, renderer);
  }
  const cached = diagrams.get(token.content);
  if (cached) return cached;
  try {
    if (token.content.length > 16000 || token.content.split("\n").length > 256) {
      throw new Error("Diagram exceeds rendering limits");
    }
    const declaration = token.content.replace(/^\s*%%[^\n]*$/gm, "").trimStart();
    if (!/^(?:(?:flowchart|graph)\s+(?:TB|TD|LR|RL|BT)\b|(?:sequenceDiagram|stateDiagram(?:-v2)?|classDiagram|erDiagram|xychart(?:-beta)?)\b)/.test(declaration)) {
      throw new Error("Unsupported diagram type");
    }
    // SVGs are images, never live page markup. No callbacks, raw HTML, remote
    // fonts or relaxed page CSP are needed to display an author's diagram.
    const svg = renderMermaidSVG(token.content, {
      bg: "#18181b", fg: "#eeeef1", accent: "#c2a2ff", font: "sans-serif",
    }).replace(/^[ \t]*@import[^\r\n]*$/gm, "");
    const width = Number(svg.match(/\bwidth="([\d.]+)"/)?.[1]);
    const height = Number(svg.match(/\bheight="([\d.]+)"/)?.[1]);
    if (!(width > 0 && height > 0 && width <= 16000 && height <= 16000)) {
      throw new Error("Invalid diagram dimensions");
    }
    const result = `<figure class="not-prose my-4 min-w-0">
<div class="overflow-x-auto rounded-box border border-base-content/10 bg-base-200 p-4" role="region" aria-label="Mermaid diagram" tabindex="0">
<img class="mx-auto block max-w-none object-contain" loading="lazy" alt="Mermaid diagram; text description available in Diagram source" width="${width}" height="${height}" src="data:image/svg+xml,${encodeURIComponent(svg)}">
</div>
<details class="mt-2"><summary class="cursor-pointer text-sm text-base-content/70 focus-visible:outline-2">Diagram source</summary>${fence(tokens, index, options, env, renderer)}</details>
</figure>\n`;
    if (diagrams.size >= 32) diagrams.delete(diagrams.keys().next().value!);
    diagrams.set(token.content, result);
    return result;
  } catch {
    return `<p class="text-sm text-base-content/70">Diagram unavailable. Source shown below.</p>\n${fence(tokens, index, options, env, renderer)}`;
  }
};

export function render(source: string) {
  return markdown.render(source);
}

export const prose =
  "prose prose-invert prose-sm max-w-[68ch] prose-headings:font-semibold prose-headings:tracking-tight prose-h1:text-xl prose-h2:text-base prose-h3:text-sm prose-a:text-primary prose-pre:bg-base-200 prose-pre:border prose-pre:border-base-content/10 prose-code:before:content-none prose-code:after:content-none";
