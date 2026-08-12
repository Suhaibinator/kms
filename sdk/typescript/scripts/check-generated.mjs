import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const protoPath = fileURLToPath(new URL("../../../proto/kms/v1/kms.proto", import.meta.url));
const generatedPath = fileURLToPath(new URL("../src/generated/kms.ts", import.meta.url));

const proto = await readFile(protoPath);
const generated = await readFile(generatedPath, "utf8").catch(() => "");
const digest = createHash("sha256").update(proto).digest("hex");
const marker = `// source-sha256: ${digest}`;

if (!generated.includes(marker)) {
  console.error("Generated protobuf bindings are stale. Run `npm run generate` and commit them.");
  process.exit(1);
}
