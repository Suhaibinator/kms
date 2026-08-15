import {
  ClassifiedReleaseError,
  createClient,
  mtlsFromFiles,
  type PolicySnapshot,
  type PreparedRelease,
  type ReleaseSnapshot,
} from "@suhaibinator/kms";

interface RuntimePolicy {
  readonly requestTimeoutMs: number;
}

let active: PolicySnapshot<RuntimePolicy> | undefined;

function requiredEnvironment(name: string): string {
  const value = process.env[name];
  if (value === undefined || value.length === 0) throw new Error(`${name} is required`);
  return value;
}

const client = createClient({
  endpoint: requiredEnvironment("KMS_ENDPOINT"),
  credentials: mtlsFromFiles(
    requiredEnvironment("KMS_CLIENT_CERT"),
    requiredEnvironment("KMS_CLIENT_KEY"),
    requiredEnvironment("KMS_SERVER_CA"),
  ),
});
const loader = await client.createReleaseLoader({ name: "runtime" });
const shutdown = new AbortController();

for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.once(signal, () => shutdown.abort(new DOMException("shutdown", "AbortError")));
}

try {
  await loader.run((snapshot: ReleaseSnapshot): PreparedRelease => {
    const raw = snapshot.parameter("request_timeout_ms")?.value();
    const requestTimeoutMs = Number(raw);
    if (!Number.isInteger(requestTimeoutMs) || requestTimeoutMs <= 0) {
      throw new ClassifiedReleaseError("config_validation_failed");
    }

    const candidate: PolicySnapshot<RuntimePolicy> = Object.freeze({
      revision: snapshot.activationRevision,
      value: Object.freeze({ requestTimeoutMs }),
    });
    return {
      // Keep commit synchronous and infallible: all parsing and validation is done above.
      commit() {
        active = candidate;
      },
      abort() {},
    };
  }, shutdown.signal);
} finally {
  loader.stop();
  await client.close();
}

// A real service reads this from request handlers while the loader is running.
void active;
