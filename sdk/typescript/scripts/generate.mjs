import { fileURLToPath } from "node:url";
import { generateProtobuf } from "./proto-generation.mjs";

const outputDirectory = fileURLToPath(new URL("../src/generated", import.meta.url));

await generateProtobuf(outputDirectory);
