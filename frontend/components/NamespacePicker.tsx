import { useMemo } from "react";
import type { Namespace } from "@/lib/types";

export interface NamespaceSelection {
  env: string;
  app: string;
}

// An env → app cascading selector fed by the namespace list. Choosing an env
// narrows the app options to that env; changing env clears an app that no
// longer belongs. Both selects are controlled by `value`.
export default function NamespacePicker({
  namespaces,
  value,
  onChange,
  disabled,
  envId = "ns-env",
  appId = "ns-app",
}: {
  namespaces: Namespace[];
  value: NamespaceSelection;
  onChange: (next: NamespaceSelection) => void;
  disabled?: boolean;
  envId?: string;
  appId?: string;
}) {
  const envs = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) set.add(ns.env);
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces]);

  const apps = useMemo(() => {
    if (!value.env) return [];
    const set = new Set<string>();
    for (const ns of namespaces) {
      if (ns.env === value.env) set.add(ns.app);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces, value.env]);

  function onEnv(env: string) {
    // Keep the app only if it still exists under the new env.
    const stillValid = namespaces.some((ns) => ns.env === env && ns.app === value.app);
    onChange({ env, app: stillValid ? value.app : "" });
  }

  return (
    <>
      <div className="field">
        <label className="field-label" htmlFor={envId}>
          Environment
        </label>
        <select
          id={envId}
          className="select"
          value={value.env}
          disabled={disabled}
          onChange={(e) => onEnv(e.target.value)}
        >
          <option value="">Select env…</option>
          {envs.map((env) => (
            <option key={env} value={env}>
              {env}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="field-label" htmlFor={appId}>
          Application
        </label>
        <select
          id={appId}
          className="select"
          value={value.app}
          disabled={disabled || !value.env}
          onChange={(e) => onChange({ env: value.env, app: e.target.value })}
        >
          <option value="">{value.env ? "Select app…" : "Select env first"}</option>
          {apps.map((app) => (
            <option key={app} value={app}>
              {app}
            </option>
          ))}
        </select>
      </div>
    </>
  );
}
