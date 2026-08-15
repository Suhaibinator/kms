export async function register(): Promise<void> {
  if (process.env.NEXT_RUNTIME === "nodejs") {
    const { kms } = await import("./kms.js");
    await kms.start();
    kms.installProcessShutdown({
      signals: ["SIGINT", "SIGTERM"],
      // Installing a signal listener suppresses Node's default termination.
      // This application explicitly restores the conventional exit status
      // only after the KMS loader/client cleanup attempt has settled.
      onCleanupComplete(signal) {
        process.exit(signal === "SIGINT" ? 130 : 143);
      },
    });
  }
}
