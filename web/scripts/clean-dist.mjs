// Empties internal/webui/dist except the committed .keep file, which keeps the
// go:embed pattern valid when the UI has not been built.
import { readdirSync, rmSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../../internal/webui/dist', import.meta.url));
mkdirSync(dist, { recursive: true });
for (const entry of readdirSync(dist)) {
  if (entry !== '.keep') rmSync(join(dist, entry), { recursive: true, force: true });
}
