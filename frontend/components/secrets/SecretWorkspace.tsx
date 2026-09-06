import type { ResourceRef } from "@/lib/api";
import SecretManager from "./SecretManager";

export { shouldOpenWorkspace as shouldOpenSecretWorkspace } from "@/lib/workspace";

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
