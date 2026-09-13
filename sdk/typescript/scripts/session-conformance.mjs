// Real-server client for internal/integration session conformance tests.
// Build this SDK first. The host supplies TLS/auth and cuts its TCP proxy to
// exercise transport reconnect while independently checking persisted state.
import assert from "node:assert/strict";
import { KmsClient, tlsFromFiles } from "../dist/index.js";

function required(name) {
  const value = process.env[`KMS_CONFORMANCE_${name}`];
  assert.ok(value, `KMS_CONFORMANCE_${name} is required`);
  return value;
}

const client = new KmsClient({
  endpoint: required("ENDPOINT"),
  token: required("TOKEN"),
  namespace: required("NAMESPACE"),
  credentials: tlsFromFiles(required("CA_FILE")),
  clientName: "typescript",
});
const loader = await client.createReleaseLoader({
  name: required("RELEASE"),
  schemaVersion: BigInt(required("SCHEMA_VERSION")),
  instanceId: required("INSTANCE"),
  reconcileIntervalMs: 100,
});
const stop = new AbortController();
process.once("SIGTERM", () => stop.abort());
process.once("SIGINT", () => stop.abort());
// SDK retry timers are intentionally unref'ed; this standalone driver has no
// application HTTP server to keep Node alive while the proxy is disconnected.
const keepAlive = setInterval(() => {}, 1_000);
let preparations = 0;
try {
  await loader.run((snapshot) => {
    preparations += 1;
    assert.equal(preparations, 1, "unchanged target must not be prepared again on reconnect");
    assert.equal(snapshot.parameter("setting")?.value(), "1");
    return { commit() {}, abort() {} };
  }, stop.signal);
} catch (error) {
  if (!stop.signal.aborted) throw error;
} finally {
  clearInterval(keepAlive);
  await client.close();
}
