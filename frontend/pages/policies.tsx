import { Pencil, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import {
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination, useFieldErrors, useLatestRequest } from "@/lib/hooks";
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

function newRule(): DraftRule {
  return { id: ++ruleID, operation: "secret:read", env: "*", app: "*" };
}

function draftRules(rules: PolicyRule[] | null | undefined): DraftRule[] {
  return rules ? rules.map((r) => ({ ...r, id: ++ruleID })) : [];
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

export default function PoliciesPage() {
  const toast = useToast();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const paging = useCursorPagination("policies");
  const { begin } = useLatestRequest();

  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  // Keys are composite (`allow-<rule id>-app`), hence the plain string form.
  const fieldErrors = useFieldErrors<string>();

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

  function openCreate() {
    fieldErrors.reset();
    setDraft(emptyDraft());
  }
  function openEdit(p: Policy) {
    fieldErrors.reset();
    setDraft({
      name: p.name,
      subject: p.subject,
      allow: draftRules(p.allow),
      deny: draftRules(p.deny),
      editing: true,
    });
  }

  async function save() {
    if (!draft) return;
    fieldErrors.markAllTouched();
    // Every problem is now rendered next to the field that has it.
    if (draftError(draft) !== null) return;
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

  return (
    <>
      <PageHeader
        title="Policies"
        subtitle="Allow / deny rules that authorize identities for operations on a namespace."
        actions={<Button onClick={openCreate}>New policy</Button>}
      />

      {loading ? (
        <TableSkeleton headers={["Name", "Subject", "Allow", "Deny", "Updated"]} />
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
            <thead>
              <tr>
                <th>Name</th>
                <th>Subject</th>
                <th>Allow</th>
                <th>Deny</th>
                <th>Updated</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {policies.map((p) => (
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
        footer={
          <>
            <Button variant="outline" onClick={() => setDraft(null)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={save} disabled={saving}>
              {saving ? <Spinner /> : null}
              {draft?.editing ? "Save changes" : "Create policy"}
            </Button>
          </>
        }
      >
        {draft ? (
          <form
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
                  className="font-mono"
                  value={draft.subject}
                  onChange={(e) => setDraft({ ...draft, subject: e.target.value })}
                  onBlur={() => fieldErrors.touch("subject")}
                  placeholder="gradethis-be"
                />
              </Field>
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
              onChange={(allow) => setDraft({ ...draft, allow })}
              visibleError={fieldErrors.shown}
              onTouch={fieldErrors.touch}
            />
            <hr className="divider" />
            <RuleEditor
              title="Deny rules"
              kind="deny"
              rules={draft.deny}
              onChange={(deny) => setDraft({ ...draft, deny })}
              visibleError={fieldErrors.shown}
              onTouch={fieldErrors.touch}
            />
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

function RuleEditor({
  title,
  kind,
  rules,
  onChange,
  visibleError,
  onTouch,
}: {
  title: string;
  /** Namespaces the touched-field keys so allow and deny rows stay distinct. */
  kind: RuleKind;
  rules: DraftRule[];
  onChange: (rules: DraftRule[]) => void;
  visibleError: (key: string, message: string | null) => string | null;
  onTouch: (key: string) => void;
}) {
  const fieldKey = (rule: DraftRule, field: RuleField) => `${kind}-${rule.id}-${field}`;
  const errorFor = (rule: DraftRule, field: RuleField) =>
    visibleError(fieldKey(rule, field), ruleFieldError(rule, field));

  function update(id: number, patch: Partial<PolicyRule>) {
    onChange(rules.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  }
  function remove(id: number) {
    onChange(rules.filter((r) => r.id !== id));
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
      {rules.length === 0 ? (
        <div className="faint text-sm mb-2">No rules.</div>
      ) : (
        <div className="stack">
          {rules.map((rule, i) => (
            <div key={rule.id} className="rule-row">
              <div className="rule-op">
                <Field label="Operation" error={errorFor(rule, "operation")}>
                  <AppSelect
                    value={rule.operation}
                    onValueChange={(operation) => update(rule.id, { operation })}
                    onBlur={() => onTouch(fieldKey(rule, "operation"))}
                    options={operationOptions(rule.operation).map((operation) => ({
                      value: operation,
                      label: operation,
                    }))}
                  />
                </Field>
              </div>
              <div className="rule-app">
                <Field label="App" error={errorFor(rule, "app")}>
                  <Input
                    className="font-mono"
                    value={rule.app}
                    onChange={(e) => update(rule.id, { app: e.target.value })}
                    onBlur={() => onTouch(fieldKey(rule, "app"))}
                    placeholder="gradethis"
                  />
                </Field>
              </div>
              <div className="rule-env">
                <Field label="Env" error={errorFor(rule, "env")}>
                  <Input
                    className="font-mono"
                    value={rule.env}
                    onChange={(e) => update(rule.id, { env: e.target.value })}
                    onBlur={() => onTouch(fieldKey(rule, "env"))}
                    placeholder="prod"
                  />
                </Field>
              </div>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                className="rule-remove"
                onClick={() => remove(rule.id)}
                aria-label={`Remove ${kind} rule ${i + 1}: ${rule.operation}`}
              >
                Remove
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
