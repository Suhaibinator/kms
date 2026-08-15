"use client";

import { useState } from "react";
import { usePublicConfig } from "@suhaibinator/kms/next/client";
import type { PublicPasswordPolicy } from "../public-policy.js";
import { validatePassword } from "./actions.js";

type PublicPasswordPolicyWire = Parameters<typeof usePublicConfig<PublicPasswordPolicy>>[0];

export function PasswordForm({ initial }: { readonly initial: PublicPasswordPolicyWire }) {
  const policy = usePublicConfig(initial);
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        void validatePassword(policy.revision.toString(10), password).then((result) => {
          if (policy.applyServerResult(result)) {
            setMessage("The password policy changed; review the updated requirement.");
          } else if (result.status === "validation_failed") {
            setMessage(result.errors.password);
          } else if (result.status === "success") {
            setMessage("Password accepted by the active server policy.");
          } else {
            setMessage("Configuration is temporarily unavailable.");
          }
        });
      }}
    >
      <label htmlFor="password">Password (minimum {policy.config.minLength} characters)</label>
      <input
        id="password"
        minLength={policy.config.minLength}
        onChange={(event) => setPassword(event.currentTarget.value)}
        type="password"
        value={password}
      />
      <button disabled={policy.isRefreshing} type="submit">
        Continue
      </button>
      <p aria-live="polite">{message}</p>
    </form>
  );
}
