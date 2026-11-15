import { rm } from "node:fs/promises";

await rm(new URL("../dist", import.meta.url), { force: true, recursive: true });
await rm(new URL("../src/generated/kms.ts", import.meta.url), { force: true });
