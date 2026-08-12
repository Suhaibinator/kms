"use client";

import { createNextKms } from "@suhaibinator/kms/next/server";

export default function InvalidClientImport() {
  return <p>{typeof createNextKms}</p>;
}
