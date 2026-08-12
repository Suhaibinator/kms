import { kms } from "../kms.js";
import { PasswordForm } from "./password-form.js";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export default async function Page() {
  const initial = await kms.readPublicPolicy();
  if (initial === undefined) {
    return <p>Configuration is not ready. Try again shortly.</p>;
  }
  return <PasswordForm initial={initial} />;
}
