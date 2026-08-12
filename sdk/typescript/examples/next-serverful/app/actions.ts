"use server";

import { kms } from "../kms.js";

export async function validatePassword(revision: string, password: string) {
  return kms.validateAtRevision(revision, { password });
}
