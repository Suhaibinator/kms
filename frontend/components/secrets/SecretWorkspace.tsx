import type { ReactNode } from "react";
import type { ResourceRef } from "@/lib/api";
import SecretManager from "./SecretManager";

export function SecretWorkspace({
  secretRef,
  context,
  onClose,
  onChanged,
  onDeleted,
}: {
  secretRef: ResourceRef | null;
  /** Where the caller came from (an alias, an application link); shown under the title. */
  context?: ReactNode;
  onClose: () => void;
  onChanged?: (ref: ResourceRef) => void;
  onDeleted?: (ref: ResourceRef) => void;
}) {
  if (!secretRef) return null;
  return (
    <SecretManager
      key={JSON.stringify([secretRef.env, secretRef.app, secretRef.key])}
      resourceRef={secretRef}
      surface="workspace"
      context={context}
      onClose={onClose}
      onChanged={onChanged}
      onDeleted={onDeleted}
    />
  );
}
