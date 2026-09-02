import { readdir, readFile, writeFile } from 'node:fs/promises';
import { brotliCompressSync, constants, gzipSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

async function htmlFiles(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await htmlFiles(path));
    else if (entry.isFile() && entry.name.endsWith('.html')) result.push(path);
  }
  return result;
}

const buildDirectory = fileURLToPath(new URL('../web/build', import.meta.url));
for (const path of await htmlFiles(buildDirectory)) {
  const source = await readFile(path, 'utf8');
  const normalized = source.replace(/[\t ]+$/gm, '');
  await writeFile(path, normalized);
  await writeFile(`${path}.gz`, gzipSync(normalized, { level: 9 }));
  await writeFile(`${path}.br`, brotliCompressSync(normalized, {
    params: { [constants.BROTLI_PARAM_QUALITY]: 11 }
  }));
}
