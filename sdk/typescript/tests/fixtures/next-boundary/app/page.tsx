import { policy } from "./policy";
import { PolicyClient } from "./policy-client";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export default async function Page() {
  const initial = await policy.readPublicPolicy();
  return initial ? <PolicyClient initial={initial} /> : <p>Unavailable</p>;
}
