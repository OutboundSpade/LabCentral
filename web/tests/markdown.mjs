// Pure renderer contracts. Run with node --experimental-strip-types.
import assert from 'node:assert/strict';
import {render} from '../src/lib/markdown.ts';
const diagram = 'flowchart LR\nWS[Workstation] --> DC[Directory and DNS]\nWS --> FS[CompanyDocs]\nWS --> WEB[Online banking]\nWEB --> DB[Bank database]\nSCORE[Scoring] --> DC\nSCORE --> FS\nSCORE --> WEB\nSCORE --> DB\nLOG[Graylog] --- SOURCES[Your chosen log sources]';
const fence = source => '```mermaid\n' + source + '\n```';
const html = render(fence(diagram));
assert.match(html, /<img /);
const svg = decodeURIComponent(html.match(/src="data:image\/svg\+xml,([^"]+)"/)[1]);
for (const label of ['Workstation', 'Directory and DNS', 'CompanyDocs', 'Online banking', 'Bank database', 'Scoring', 'Graylog', 'Your chosen log sources']) assert(svg.includes(label), label);
assert(!svg.includes('@import'), 'diagram requested external fonts');
assert.match(html, /<details/);
assert.equal(render(fence(diagram)), html, 'cached rendering changed');
assert.match(render('```bash\ncat /var/log/auth.log\n```'), /<pre><code class="language-bash">cat/);
assert(!render('<script>alert(1)</script>').includes('<script>'));
for (const source of ['not a diagram', 'x'.repeat(16001), 'flowchart TD\n' + 'A-->B\n'.repeat(257)]) {
  const failed = render(fence(source));
  assert.match(failed, /Diagram unavailable/);
  assert.match(failed, /<pre><code class="language-mermaid">/);
}
for (const source of ['sequenceDiagram\nA->>B: Request\nB-->>A: Response', 'stateDiagram-v2\n[*] --> Ready\nReady --> [*]']) assert.match(render(fence(source)), /<img /);
const hostile = render(fence('flowchart LR\nA["<img src=https://outside.invalid/x onerror=alert(1)>"] --> B'));
assert(!hostile.includes('<img src=https://outside.invalid'));
assert(!hostile.includes('<script>'));
console.log('PASS diagram labels, inert SVG image, bounded rendering, fallback, ordinary code and raw HTML');
