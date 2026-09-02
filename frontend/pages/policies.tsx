import { Copy, Pencil, Trash2 } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { headerLabels, SortHeaderRow, useSort } from "@/components/SortableTable";
import {
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  TableSkeleton,
  TableSummary,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useCursorPagination, useFieldErrors, useLatestRequest, useNamespaces } from "@/lib/hooks";
import type { SortColumn } from "@/lib/sort";
import type { Namespace } from "@/lib/types";
import { POLICY_OPERATIONS, type Policy, type PolicyRule } from "@/lib/types";
import {
  firstError,
  validatePolicyName,
  validatePolicyOperation,
  validatePolicyRuleApp,
  validatePolicyRuleEnv,
  validatePolicySubject,
} from "@/lib/validation";

/**
 * A rule being edited carries a client-side id so React keys and touched-field
 * keys follow the rule when an earlier one is removed; `save()` strips it.
 */
type DraftRule = PolicyRule & { id: number };

interface Draft {
  name: string;
  subject: string;
  allow: DraftRule[];
  deny: DraftRule[];
  editing: boolean;
}

type RuleKind = "allow" | "deny";
type RuleField = keyof PolicyRule;

let ruleID = 0;

function emptyDraft(): Draft {
  return { name: "", subject: "", allow: [], deny: [], editing: false };
}

/**
 * A new rule has no operation, so it cannot be saved without a considered
 * choice; env/app default to "any" because that is the common grant shape.
 */
function newRule(): DraftRule {
  return { id: ++ruleID, operation: "", env: "*", app: "*" };
}

function draftRules(rules: PolicyRule[] | null | undefined): DraftRule[] {
  return rules ? rules.map((r) => ({ ...r, id: ++ruleID })) : [];
}

/** The rules as the server sees them, so two drafts can be compared regardless of ids. */
function draftShape(draft: Draft): string {
  const strip = (rules: DraftRule[]) =>
    rules.map(({ operation, env, app }) => ({ operation, env, app }));
  return JSON.stringify({
    name: draft.name,
    subject: draft.subject,
    allow: strip(draft.allow),
    deny: strip(draft.deny),
  });
}

/**
 * Options for the operation picker. An operation the picker does not know — a
 * server that has learned a new one, say — is offered as-is, so editing an
 * unrelated part of the policy cannot silently rewrite it.
 */
function operationOptions(current: string): string[] {
  if (current === "" || POLICY_OPERATIONS.includes(current)) return POLICY_OPERATIONS;
  return [current, ...POLICY_OPERATIONS];
}

/**
 * Validation message for one component of a rule. env and app are checked
 * trimmed because the form sends them trimmed; an empty value is legal and
 * means "any", exactly like "*" (`policy.normalizeLabel`).
 */
function ruleFieldError(rule: PolicyRule, field: RuleField): string | null {
  switch (field) {
    case "operation":
      return validatePolicyOperation(rule.operation);
    case "env":
      return validatePolicyRuleEnv(rule.env.trim());
    case "app":
      return validatePolicyRuleApp(rule.app.trim());
  }
}

/** The first message that would stop the server from accepting the draft. */
function draftError(draft: Draft): string | null {
  return firstError(
    validatePolicyName(draft.name),
    validatePolicySubject(draft.subject.trim()),
    ...[...draft.allow, ...draft.deny].flatMap((rule) => [
      ruleFieldError(rule, "operation"),
      ruleFieldError(rule, "env"),
      ruleFieldError(rule, "app"),
    ]),
  );
}

// --- effective permissions ---------------------------------------------------
//
// Mirrors the server's matching for the summary panel: "" and "*" both mean
// any label, and an operation pattern `x:*` covers every `x:` operation.

function isAnyLabel(label: string): boolean {
  const trimmed = label.trim();
  return trimmed === "" || trimmed === "*";
}

/** Whether a rule's label component (env or app) covers another rule's. */
function labelCovers(wide: string, narrow: string): boolean {
  return isAnyLabel(wide) || wide.trim() === narrow.trim();
}

/** Whether `pattern` (an operation or wildcard) covers `operation`. */
export function operationCovers(pattern: string, operation: string): boolean {
  if (pattern === "*") return true;
  if (pattern.endsWith(":*")) {
    const category = pattern.slice(0, -2);
    return operation === pattern || operation.startsWith(`${category}:`);
  }
  return pattern === operation;
}

/** Whether a deny rule cancels an allow rule (same or wider scope, covering operation). */
export function denyCancels(deny: PolicyRule, allow: PolicyRule): boolean {
  return (
    labelCovers(deny.app, allow.app) &&
    labelCovers(deny.env, allow.env) &&
    operationCovers(deny.operation, allow.operation)
  );
}

export function scopeLabel(rule: PolicyRule): string {
  const app = isAnyLabel(rule.app) ? "any" : rule.app.trim();
  const env = isAnyLabel(rule.env) ? "any" : rule.env.trim();
  return `${env}/${app}`;
}

export interface EffectiveScope {
  scope: string;
  operations: Array<{ operation: string; cancelled: boolean }>;
}

export interface EffectiveSummary {
  allows: EffectiveScope[];
  denies: EffectiveScope[];
}

/** The read-only "what does this policy grant" data behind the summary panel. */
export function effectiveSummary(draft: {
  allow: readonly PolicyRule[];
  deny: readonly PolicyRule[];
}): EffectiveSummary {
  const group = (
    rules: readonly PolicyRule[],
    cancelled: (rule: PolicyRule) => boolean,
  ): EffectiveScope[] => {
    const scopes = new Map<string, EffectiveScope>();
    for (const rule of rules) {
      if (rule.operation === "") continue;
      const scope = scopeLabel(rule);
      const entry = scopes.get(scope) ?? { scope, operations: [] };
      if (!entry.operations.some((item) => item.operation === rule.operation)) {
        entry.operations.push({ operation: rule.operation, cancelled: cancelled(rule) });
      }
      scopes.set(scope, entry);
    }
    return [...scopes.values()];
  };
  const denies = draft.deny.filter((rule) => rule.operation !== "");
  return {
    allows: group(draft.allow, (allow) => denies.some((deny) => denyCancels(deny, allow))),
    denies: group(draft.deny, () => false),
  };
}

function EffectivePermissions({ draft }: { draft: Draft }) {
  const summary = useMemo(() => effectiveSummary(draft), [draft]);
  const empty = summary.allows.length === 0 && summary.denies.length === 0;
  return (
    <section className="info-panel mt-4 text-sm" aria-label="Effective permissions">
      <strong>Effective permissions</strong>
      {empty ? (
        <div className="faint">No rules yet.</div>
      ) : (
        <>
          {summary.allows.map((scope) => (
            <div key={`allow-${scope.scope}`}>
              Allows on <span className="mono">{scope.scope}</span>:{" "}
              {scope.operations.map((item, index) => (
                <span key={item.operation}>
                  {index > 0 ? ", " : ""}
                  <span className="mono">{item.operation}</span>
                  {item.cancelled ? <span className="faint"> (cancelled by deny)</span> : null}
                </span>
              ))}
            </div>
          ))}
          {summary.denies.map((scope) => (
            <div key={`deny-${scope.scope}`}>
              Denied (overrides allow) on <span className="mono">{scope.scope}</span>:{" "}
              {scope.operations.map((item, index) => (
                <span key={item.operation}>
                  {index > 0 ? ", " : ""}
                  <span className="mono">{item.operation}</span>
                </span>
              ))}
            </div>
          ))}
        </>
      )}
    </section>
  );
}

const MAX_IDENTITY_PAGES = 10;

// Module scope so the sort controller's memos stay stable across renders.
const COLUMNS: ReadonlyArray<SortColumn<Policy>> = [
  { id: "name", label: "Name", value: (p) => p.name },
  { id: "subject", label: "Subject", value: (p) => p.subject },
  { id: "allow", label: "Allow", value: (p) => p.allow?.length ?? 0 },
  { id: "deny", label: "Deny", value: (p) => p.deny?.length ?? 0 },
  { id: "updated", label: "Updated", value: (p) => p.updated_at_unix_ms },
];

const PAGE_SORT_HINT = "Sorts the policies loaded on this page, not every policy.";

export default function PoliciesPage() {
  const toast = useToast();
  const sort = useSort<Policy>("/policies", COLUMNS);
  const { namespaces, loading: namespacesLoading } = useNamespaces();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const paging = useCursorPagination("policies");
  const { begin } = useLatestRequest();

  const [draft, setDraft] = useState<Draft | null>(null);
  // What the editor opened with, so dismissal can tell an untouched draft apart.
  const [opened, setOpened] = useState("");
  const [saving, setSaving] = useState(false);
  // Keys are composite (`allow-<rule id>-app`), hence the plain string form.
  const fieldErrors = useFieldErrors<string>();
  const { formRef, requestFocus } = useFocusFirstInvalid();
  const nameRef = useRef<HTMLInputElement>(null);
  const subjectRef = useRef<HTMLInputElement>(null);
  const [identityNames, setIdentityNames] = useState<string[]>([]);
  const subjectListId = useId();

  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const { setNextToken } = paging;
  /** Resolves to the fetched page, or null when the load failed or was superseded. */
  const load = useCallback(
    async (token: string): Promise<Policy[] | null> => {
      const run = begin();
      setLoading(true);
      try {
        const res = await api.listPolicies(100, token || undefined, { signal: run.signal });
        if (!run.current) return null;
        const list = res.policies ?? [];
        setPolicies(list);
        setNextToken(res.next_page_token ?? "");
        return list;
      } catch (err) {
        if (run.current && !isAbortError(err)) toast.error(err, "Failed to load policies");
        return null;
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [begin, setNextToken, toast],
  );

  useEffect(() => {
    void load(paging.pageToken);
  }, [load, paging.pageToken]);

  // Identity names for the Subject combobox, fetched per editor open. A
  // failure is silent: the field still accepts any name.
  const editorOpen = draft !== null;
  useEffect(() => {
    if (!editorOpen) return;
    const controller = new AbortController();
    void (async () => {
      const names: string[] = [];
      let token = "";
      try {
        for (let page = 0; page < MAX_IDENTITY_PAGES; page += 1) {
          const res = await api.listIdentities(200, token || undefined, {
            signal: controller.signal,
          });
          names.push(...(res.identities ?? []).map((identity) => identity.name));
          token = res.next_page_token ?? "";
          if (!token) break;
        }
        if (!controller.signal.aborted) setIdentityNames(names.sort((a, b) => a.localeCompare(b)));
      } catch {
        // Leave whatever was loaded; the combobox is a convenience.
      }
    })();
    return () => controller.abort();
  }, [editorOpen]);

  function openCreate() {
    fieldErrors.reset();
    const next = emptyDraft();
    setOpened(draftShape(next));
    setDraft(next);
  }
  function openEdit(p: Policy) {
    fieldErrors.reset();
    const next: Draft = {
      name: p.name,
      subject: p.subject,
      allow: draftRules(p.allow),
      deny: draftRules(p.deny),
      editing: true,
    };
    setOpened(draftShape(next));
    setDraft(next);
  }

  async function save() {
    if (!draft) return;
    fieldErrors.markAllTouched();
    // Every problem is now rendered next to the field that has it.
    if (draftError(draft) !== null) {
      requestFocus();
      return;
    }
    // env/app are sent as typed: the server normalizes "" and "*" alike to "*"
    // (`policy.normalizeLabel`), so a blank component means "any" and the rule
    // must not be dropped on its way out.
    const clean = (rules: DraftRule[]): PolicyRule[] =>
      rules.map((r) => ({
        operation: r.operation,
        env: r.env.trim(),
        app: r.app.trim(),
      }));

    const policy: Policy = {
      name: draft.name.trim(),
      subject: draft.subject.trim(),
      allow: clean(draft.allow),
      deny: clean(draft.deny),
      created_at_unix_ms: 0,
      updated_at_unix_ms: 0,
    };

    setSaving(true);
    try {
      if (draft.editing) {
        await api.updatePolicy(policy);
        toast.success("Policy updated", policy.name);
      } else {
        await api.createPolicy(policy);
        toast.success("Policy created", policy.name);
      }
      setDraft(null);
      paging.reset();
      await load("");
    } catch (err) {
      toast.error(err, "Failed to save policy");
    } finally {
      setSaving(false);
    }
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deletePolicy(deleteTarget);
      toast.success("Policy deleted", deleteTarget);
      setDeleteTarget(null);
      // Deleting the last row on a later page would otherwise leave an empty page.
      const remaining = await load(paging.pageToken);
      if (remaining !== null && remaining.length === 0 && paging.hasPrevious) paging.previous();
    } catch (err) {
      toast.error(err, "Failed to delete policy");
    } finally {
      setDeleting(false);
    }
  }

  const dirty = draft !== null && draftShape(draft) !== opened;
  const subjectOptions = useMemo(
    () => ["*", ...identityNames.filter((name) => name !== "*")],
    [identityNames],
  );

  return (
    <>
      <PageHeader
        title="Policies"
        subtitle="Allow / deny rules that authorize identities for operations on a namespace."
        actions={<Button onClick={openCreate}>New policy</Button>}
      />

      {loading ? (
        <TableSkeleton headers={headerLabels(COLUMNS)} />
      ) : policies.length === 0 ? (
        <EmptyState
          icon={<Icon.policy size={20} />}
          title="No policies yet"
          actions={<Button onClick={openCreate}>New policy</Button>}
        >
          Create a policy to grant an identity access to parameters or secrets.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <TableSummary
              shown={policies.length}
              noun="policies"
              hint={sort.sort ? PAGE_SORT_HINT : undefined}
            />
            <thead>
              <SortHeaderRow controller={sort} hint={PAGE_SORT_HINT} after={<th />} />
            </thead>
            <tbody>
              {sort.apply(policies).map((p) => (
                <tr key={p.name}>
                  <td className="mono">{p.name}</td>
                  <td className="mono">{p.subject}</td>
                  <td>{p.allow?.length ?? 0}</td>
                  <td>{p.deny?.length ?? 0}</td>
                  <td className="nowrap">{formatUnixMs(p.updated_at_unix_ms)}</td>
                  <td>
                    <div className="row-actions">
                      <Button variant="outline" size="sm" onClick={() => openEdit(p)}>
                        <Pencil size={14} aria-hidden />
                        Edit
                      </Button>
                      <Button
                        variant="destructive"
                        size="sm"
                        onClick={() => setDeleteTarget(p.name)}
                      >
                        <Trash2 size={14} aria-hidden />
                        Delete
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Pagination
        hasNext={paging.hasNext}
        onNext={paging.next}
        hasPrevious={paging.hasPrevious}
        onPrevious={paging.previous}
        onReset={paging.reset}
        showReset={paging.hasPrevious}
        page={paging.page}
      />

      <Modal
        open={draft !== null}
        wide
        title={draft?.editing ? `Edit policy: ${draft.name}` : "New policy"}
        onClose={() => setDraft(null)}
        dismissible={!saving}
        dirty={dirty}
        initialFocus={draft?.editing ? subjectRef : nameRef}
        footer={(close) => (
          <>
            <Button variant="outline" onClick={close} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={save} loading={saving}>
              {draft?.editing ? "Save changes" : "Create policy"}
            </Button>
          </>
        )}
      >
        {draft ? (
          <form
            ref={formRef}
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <div className="form-row">
              <Field
                label="Name"
                hint={draft.editing ? "Cannot be changed." : "Unique policy name."}
                error={fieldErrors.shown("name", validatePolicyName(draft.name))}
              >
                <Input
                  ref={nameRef}
                  className="font-mono"
                  value={draft.name}
                  disabled={draft.editing}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  onBlur={() => fieldErrors.touch("name")}
                  placeholder="gradethis-read"
                />
              </Field>
              <Field
                label="Subject"
                hint="The identity this policy applies to, or * for every identity."
                error={fieldErrors.shown("subject", validatePolicySubject(draft.subject.trim()))}
              >
                <Input
                  ref={subjectRef}
                  className="font-mono"
                  value={draft.subject}
                  list={subjectListId}
                  onChange={(e) => setDraft({ ...draft, subject: e.target.value })}
                  onBlur={() => fieldErrors.touch("subject")}
                  placeholder="gradethis-be"
                  autoComplete="off"
                />
              </Field>
              <datalist id={subjectListId}>
                {subjectOptions.map((name) => (
                  <option key={name} value={name} />
                ))}
              </datalist>
            </div>

            <div className="info-panel mb-4">
              Deny rules always override allow rules. Environments are application-owned; use
              <span className="mono"> *</span> for either component to match any. A grant covers the
              whole namespace — every key in it — since the namespace is the unit of authorization.
            </div>

            <RuleEditor
              title="Allow rules"
              kind="allow"
              rules={draft.allow}
              namespaces={namespaces}
              namespacesLoading={namespacesLoading}
              onChange={(allow) => setDraft({ ...draft, allow })}
              visibleError={fieldErrors.shown}
              onTouch={fieldErrors.touch}
            />
            <hr className="divider" />
            <RuleEditor
              title="Deny rules"
              kind="deny"
              rules={draft.deny}
              namespaces={namespaces}
              namespacesLoading={namespacesLoading}
              onChange={(deny) => setDraft({ ...draft, deny })}
              visibleError={fieldErrors.shown}
              onTouch={fieldErrors.touch}
            />
            <EffectivePermissions draft={draft} />
            {/* Lets Enter submit from any input; the visible submit lives in the footer. */}
            <button type="submit" className="sr-only" tabIndex={-1} aria-hidden />
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete policy?"
        danger
        message={
          <>
            Delete policy <span className="mono">{deleteTarget}</span>? Identities relying on it
            will lose the access it granted.
          </>
        }
        confirmLabel="Delete policy"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  );
}

/** Non-blocking hint when a typed app/env matches no existing namespace. */
function unknownNamespaceWarning(
  rule: PolicyRule,
  field: "app" | "env",
  namespaces: Namespace[],
  loading: boolean,
): string | null {
  if (loading) return null;
  const app = rule.app.trim();
  const env = rule.env.trim();
  if (field === "app") {
    if (isAnyLabel(app)) return null;
    return namespaces.some((ns) => ns.app === app)
      ? null
      : `No namespace for application ${app} exists yet; this rule will match nothing until it does.`;
  }
  if (isAnyLabel(env)) return null;
  const inApp = isAnyLabel(app)
    ? namespaces.some((ns) => ns.env === env)
    : namespaces.some((ns) => ns.env === env && ns.app === app);
  if (inApp) return null;
  return isAnyLabel(app)
    ? `No environment named ${env} exists yet; this rule will match nothing until it does.`
    : `No namespace named ${env}/${app} exists yet; this rule will match nothing until it does.`;
}

function RuleEditor({
  title,
  kind,
  rules,
  namespaces,
  namespacesLoading,
  onChange,
  visibleError,
  onTouch,
}: {
  title: string;
  /** Namespaces the touched-field keys so allow and deny rows stay distinct. */
  kind: RuleKind;
  rules: DraftRule[];
  namespaces: Namespace[];
  namespacesLoading: boolean;
  onChange: (rules: DraftRule[]) => void;
  visibleError: (key: string, message: string | null) => string | null;
  onTouch: (key: string) => void;
}) {
  const listId = useId();
  const fieldKey = (rule: DraftRule, field: RuleField) => `${kind}-${rule.id}-${field}`;
  const errorFor = (rule: DraftRule, field: RuleField) =>
    visibleError(fieldKey(rule, field), ruleFieldError(rule, field));

  const apps = useMemo(
    () => ["*", ...[...new Set(namespaces.map((ns) => ns.app))].sort((a, b) => a.localeCompare(b))],
    [namespaces],
  );
  const envsFor = (app: string): string[] => {
    const trimmed = app.trim();
    const scoped = namespaces.some((ns) => ns.app === trimmed)
      ? namespaces.filter((ns) => ns.app === trimmed)
      : namespaces;
    return ["*", ...[...new Set(scoped.map((ns) => ns.env))].sort((a, b) => a.localeCompare(b))];
  };

  function update(id: number, patch: Partial<PolicyRule>) {
    onChange(rules.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  }
  function remove(id: number) {
    onChange(rules.filter((r) => r.id !== id));
  }
  function duplicate(id: number) {
    const index = rules.findIndex((r) => r.id === id);
    if (index === -1) return;
    const copy: DraftRule = { ...rules[index], id: ++ruleID };
    onChange([...rules.slice(0, index + 1), copy, ...rules.slice(index + 1)]);
  }
  function add() {
    onChange([...rules, newRule()]);
  }

  return (
    <div>
      <div className="between mb-2">
        <div className="field-label">{title}</div>
        <Button type="button" variant="outline" size="sm" onClick={add}>
          Add rule
        </Button>
      </div>
      <datalist id={`${listId}-apps`}>
        {apps.map((app) => (
          <option key={app} value={app} />
        ))}
      </datalist>
      {rules.length === 0 ? (
        <div className="faint text-sm mb-2">No rules.</div>
      ) : (
        <div className="stack">
          {rules.map((rule, i) => {
            const operationName = rule.operation || "unset operation";
            const appWarning = unknownNamespaceWarning(rule, "app", namespaces, namespacesLoading);
            const envWarning = unknownNamespaceWarning(rule, "env", namespaces, namespacesLoading);
            const envListId = `${listId}-envs-${rule.id}`;
            return (
              <div key={rule.id} className="rule-row">
                <div className="rule-op">
                  <Field label="Operation" error={errorFor(rule, "operation")}>
                    <AppSelect
                      value={rule.operation}
                      onValueChange={(operation) => update(rule.id, { operation })}
                      onBlur={() => onTouch(fieldKey(rule, "operation"))}
                      placeholder="Select operation…"
                      options={operationOptions(rule.operation).map((operation) => ({
                        value: operation,
                        label: operation,
                      }))}
                    />
                  </Field>
                </div>
                <div className="rule-app">
                  <Field
                    label="App"
                    error={errorFor(rule, "app")}
                    hint={appWarning ? <span className="text-warning">{appWarning}</span> : null}
                  >
                    <Input
                      className="font-mono"
                      value={rule.app}
                      list={`${listId}-apps`}
                      autoComplete="off"
                      onChange={(e) => update(rule.id, { app: e.target.value })}
                      onBlur={() => onTouch(fieldKey(rule, "app"))}
                      placeholder="gradethis"
                    />
                  </Field>
                </div>
                <div className="rule-env">
                  <Field
                    label="Env"
                    error={errorFor(rule, "env")}
                    hint={envWarning ? <span className="text-warning">{envWarning}</span> : null}
                  >
                    <Input
                      className="font-mono"
                      value={rule.env}
                      list={envListId}
                      autoComplete="off"
                      onChange={(e) => update(rule.id, { env: e.target.value })}
                      onBlur={() => onTouch(fieldKey(rule, "env"))}
                      placeholder="prod"
                    />
                  </Field>
                  <datalist id={envListId}>
                    {envsFor(rule.app).map((env) => (
                      <option key={env} value={env} />
                    ))}
                  </datalist>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="rule-remove"
                  onClick={() => duplicate(rule.id)}
                  aria-label={`Duplicate ${kind} rule ${i + 1}`}
                >
                  <Copy size={14} aria-hidden />
                  Duplicate
                </Button>
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  className="rule-remove"
                  onClick={() => remove(rule.id)}
                  aria-label={`Remove ${kind} rule ${i + 1}: ${operationName}`}
                >
                  Remove
                </Button>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
