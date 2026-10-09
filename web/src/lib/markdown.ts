import MarkdownIt from "markdown-it";

// Raw HTML stays disabled: lesson and handover text is rendered as Markdown only.
const markdown = new MarkdownIt({ html: false, linkify: false });

export function render(source: string) {
  return markdown.render(source);
}

export const prose =
  "prose prose-invert prose-sm max-w-[68ch] prose-headings:font-semibold prose-headings:tracking-tight prose-h1:text-xl prose-h2:text-base prose-h3:text-sm prose-a:text-primary prose-pre:bg-base-200 prose-pre:border prose-pre:border-base-content/10 prose-code:before:content-none prose-code:after:content-none";
