import { useMemo } from "react";
import { Field } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import type { Namespace } from "@/lib/types";

export interface NamespaceSelection {
  env: string;
  app: string;
}

// An app → environment cascading selector fed by the namespace list. An
// environment belongs to an application; it is not a KMS-wide choice. Changing
// application therefore clears an environment that the new application does
// not have. Both selects are controlled by `value`.
export default function NamespacePicker({
  namespaces,
  value,
  onChange,
  disabled,
  envId = "ns-env",
  appId = "ns-app",
  appError,
  envError,
}: {
  namespaces: Namespace[];
  value: NamespaceSelection;
  onChange: (next: NamespaceSelection) => void;
  disabled?: boolean;
  envId?: string;
  appId?: string;
  /** Validation message for the application select (rendered by its Field). */
  appError?: string | null;
  /** Validation message for the environment select (rendered by its Field). */
  envError?: string | null;
}) {
  const apps = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) set.add(ns.app);
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces]);

  const envs = useMemo(() => {
    if (!value.app) return [];
    const set = new Set<string>();
    for (const ns of namespaces) {
      if (ns.app === value.app) set.add(ns.env);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces, value.app]);

  // A value that is not in the list (deep link to a deleted app, or the list has
  // not loaded yet) still renders as the selected option instead of a blank
  // trigger, so the user can see what the URL asked for.
  const appOptions = useMemo(() => {
    const options = apps.map((app) => ({ value: app, label: app }));
    if (value.app && !apps.includes(value.app)) {
      options.unshift({ value: value.app, label: `${value.app} (not found)` });
    }
    return options;
  }, [apps, value.app]);

  const envOptions = useMemo(() => {
    const options = envs.map((env) => ({ value: env, label: env }));
    if (value.env && !envs.includes(value.env)) {
      options.unshift({ value: value.env, label: `${value.env} (not found)` });
    }
    return options;
  }, [envs, value.env]);

  function onApp(app: string) {
    // Keep the environment only if it exists under the new application.
    const stillValid = namespaces.some((ns) => ns.app === app && ns.env === value.env);
    onChange({ app, env: stillValid ? value.env : "" });
  }

  return (
    <>
      <Field label="Application" htmlFor={appId} error={appError}>
        <AppSelect
          id={appId}
          value={value.app}
          disabled={disabled}
          onValueChange={onApp}
          placeholder="Select application…"
          options={appOptions}
        />
      </Field>
      <Field label="Environment" htmlFor={envId} error={envError}>
        <AppSelect
          id={envId}
          value={value.env}
          disabled={disabled || !value.app}
          onValueChange={(env) => onChange({ app: value.app, env })}
          placeholder={value.app ? "Select environment…" : "Select application first"}
          options={envOptions}
        />
      </Field>
    </>
  );
}
