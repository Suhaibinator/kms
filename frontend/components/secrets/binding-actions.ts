import type { SecretVersion } from "@/lib/types";

export type BindingActionKind = "bind" | "unbind" | "rotate" | "purge";

/** Button labels: verbs only. The state vocabulary lives in SecretBadges. */
export const BINDING_ACTION_LABELS: Record<BindingActionKind, string> = {
  bind: "Bind",
  unbind: "Unbind",
  rotate: "Rotate key",
  purge: "Purge cohort",
};

/**
 * Which binding-key actions a version offers, in display order. Bind, Unbind
 * and Rotate clone the current version, so only it offers them; a cohort purge
 * reaches any bound version an administrator can see. The version row and the
 * metadata card both read this, so the two surfaces can never disagree.
 */
export function bindingActions(
  version: SecretVersion,
  { isCurrent, canPurge }: { isCurrent: boolean; canPurge: boolean },
): BindingActionKind[] {
  if (version.state === "destroyed") return [];
  const actions: BindingActionKind[] = [];
  if (isCurrent) actions.push(version.bound ? "unbind" : "bind");
  if (isCurrent && version.bound) actions.push("rotate");
  if (version.bound && canPurge) actions.push("purge");
  return actions;
}
