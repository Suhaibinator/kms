import type { ReactNode } from "react";
import type { ResourceRef } from "@/lib/api";
import ParameterManager from "./ParameterManager";

export function ParameterWorkspace({
  parameterRef,
  context,
  onClose,
  onChanged,
  onDeleted,
}: {
  parameterRef: ResourceRef | null;
  /** Where the caller came from (an alias, an application link); shown under the title. */
  context?: ReactNode;
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
      context={context}
      onClose={onClose}
      onChanged={onChanged}
      onDeleted={onDeleted}
    />
  );
}
