import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { generateProtobuf, trackedGeneratedFile } from "./proto-generation.mjs";

const temporaryDirectory = await mkdtemp(join(tmpdir(), "kms-typescript-protobuf-"));

try {
  const regeneratedFile = await generateProtobuf(temporaryDirectory);
  const [tracked, regenerated] = await Promise.all([
    readFile(trackedGeneratedFile).catch((error) => {
      if (error?.code === "ENOENT") return undefined;
      throw error;
    }),
    readFile(regeneratedFile),
  ]);

  if (tracked === undefined || !tracked.equals(regenerated)) {
    console.error(
      "Generated protobuf bindings are stale. Run `npm run generate` and commit the result.",
    );
    process.exitCode = 1;
  }
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}
