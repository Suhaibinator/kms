import { createClient, mtlsFromFiles, ParameterValue, SecretValue } from "@suhaibinator/kms";

function requiredEnvironment(name: string): string {
  const value = process.env[name];
  if (value === undefined || value.length === 0) {
    throw new Error(`${name} is required`);
  }
  return value;
}

const client = createClient({
  endpoint: requiredEnvironment("KMS_ENDPOINT"),
  credentials: mtlsFromFiles(
    requiredEnvironment("KMS_CLIENT_CERT_FILE"),
    requiredEnvironment("KMS_CLIENT_KEY_FILE"),
    requiredEnvironment("KMS_CA_FILE"),
  ),
  // Omit this for an identity bound to its home namespace.
  ...(process.env.KMS_NAMESPACE ? { namespace: process.env.KMS_NAMESPACE } : {}),
});

const config = {
  databasePassword: new SecretValue("database-password", {
    ...(process.env.KMS_DATABASE_PASSWORD_TOKEN
      ? { token: process.env.KMS_DATABASE_PASSWORD_TOKEN }
      : {}),
    envVar: "DATABASE_PASSWORD",
  }),
  requestTimeoutMs: new ParameterValue("request-timeout-ms", {
    default: "3000",
  }),
};

try {
  await client.resolve(config);
  const password = config.databasePassword.bytes();
  try {
    // Pass the bytes directly to the library that needs them. Do not log them.
    process.stdout.write(`loaded ${password.byteLength} secret bytes\n`);
  } finally {
    password.fill(0);
  }

  const unsubscribe = config.requestTimeoutMs.onChange((_oldValue, newValue) => {
    process.stdout.write(`request timeout changed to ${newValue} ms\n`);
  });
  process.stdout.write(`request timeout is ${config.requestTimeoutMs.get()} ms\n`);

  unsubscribe();
  await config.requestTimeoutMs.dispose();
} finally {
  await client.close();
}
