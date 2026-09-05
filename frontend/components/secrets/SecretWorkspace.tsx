import type { MouseEvent } from "react";
import type { ResourceRef } from "@/lib/api";
import SecretManager from "./SecretManager";

export function shouldOpenSecretWorkspace(event: MouseEvent<HTMLElement>): boolean {
  if (
    event.defaultPrevented ||
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  ) {
    return false;
  }
  event.preventDefault();
  return true;
}

export function SecretWorkspace({
  secretRef,
  onClose,
  onChanged,
  onDeleted,
}: {
  secretRef: ResourceRef | null;
  onClose: () => void;
  onChanged?: (ref: ResourceRef) => void;
  onDeleted?: (ref: ResourceRef) => void;
}) {
  if (!secretRef) return null;
  return (
    <SecretManager
      resourceRef={secretRef}
      surface="workspace"
      onClose={onClose}
      onChanged={onChanged}
      onDeleted={onDeleted}
    />
  );
}
