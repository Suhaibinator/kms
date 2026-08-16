// `next build` wipes frontend/out, taking the tracked .gitkeep with it. That
// file is what makes the //go:embed directive in frontend_embed.go resolve on a
// fresh clone before the UI has ever been built, so put it back after every
// build — otherwise the build leaves a spurious deletion in the working tree
// and committing it breaks every Go job in CI.
//
// `make frontend` restores the placeholder itself; this covers the equally
// common `npm run build` / `npm run check` path, which bypasses the Makefile.

import { closeSync, mkdirSync, openSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const outDir = join(dirname(dirname(fileURLToPath(import.meta.url))), "out");

mkdirSync(outDir, { recursive: true });
closeSync(openSync(join(outDir, ".gitkeep"), "a"));
