import { kms } from "../../../../kms.js";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";
export const GET = kms.createPublicConfigGET({ cache: "no-store" });
