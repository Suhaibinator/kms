import type { ResourceRef } from "@/lib/api";
import ParameterManager from "./ParameterManager";

export { shouldOpenWorkspace as shouldOpenParameterWorkspace } from "@/lib/workspace";

export function ParameterWorkspace({
  parameterRef,
  onClose,
  onChanged,
  onDeleted,
}: {
  parameterRef: ResourceRef | null;
  onClose: () => void;
  onChanged?: (ref: ResourceRef) => void;
  onDeleted?: (ref: ResourceRef) => void;
}) {
  if (!parameterRef) return null;
  return (
    <ParameterManager
      key={JSON.stringify([parameterRef.env, parameterRef.app, parameterRef.key])}
      resourceRef={parameterRef}
      surface="workspace"
      onClose={onClose}
      onChanged={onChanged}
      onDeleted={onDeleted}
    />
  );
}
