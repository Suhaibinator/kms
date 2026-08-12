export async function register(): Promise<void> {
  if (process.env.NEXT_RUNTIME === "nodejs") {
    const { policy } = await import("./app/policy");
    await policy.start();
  }
}
