export async function register(): Promise<void> {
  if (process.env.NEXT_RUNTIME === "nodejs") {
    const { kms } = await import("./kms.js");
    await kms.start();
    kms.installProcessShutdown();
  }
}
