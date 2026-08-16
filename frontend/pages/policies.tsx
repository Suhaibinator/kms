import { Button } from "@/components/ui/button";
import { useCallback, useEffect, useRef, useState } from "react";
import { Pencil, Trash2 } from "lucide-react";
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
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { POLICY_OPERATIONS, type Policy, type PolicyRule } from "@/lib/types";
import {
  firstError,
  validatePolicyName,
  validatePolicyOperation,
  validatePolicyRuleApp,
  validatePolicyRuleEnv,
  validatePolicySubject,
} from "@/lib/validation";

interface Draft {
  name: string;
  subject: string;
  allow: PolicyRule[];
  deny: PolicyRule[];
  editing: boolean;
}

type RuleKind = "allow" | "deny";
type RuleField = keyof PolicyRule;

function emptyDraft(): Draft {
  return { name: "", subject: "", allow: [], deny: [], editing: false };
}

function newRule(): PolicyRule {
  return { operation: "secret:read", env: "*", app: "*" };
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
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");
  const requestRef = useRef<AbortController | null>(null);

  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  // A field's message stays hidden until the user has left it, so a half-typed
  // value is never called wrong. Attempting to save reveals everything at once.
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set());
  const [submitted, setSubmitted] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(
    async (token: string) => {
      requestRef.current?.abort();
      const controller = new AbortController();
      requestRef.current = controller;
      setLoading(true);
      try {
        const res = await api.listPolicies(100, token || undefined, {
          signal: controller.signal,
        });
        if (requestRef.current !== controller) return;
        setPolicies(res.policies ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        if (!isAbortError(err)) toast.error(err, "Failed to load policies");
      } finally {
        if (requestRef.current === controller) {
          requestRef.current = null;
          setLoading(false);
        }
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken);
    return () => {
      requestRef.current?.abort();
      requestRef.current = null;
    };
  }, [load, pageToken]);

  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goPrevious() {
    const previous = pageStack[pageStack.length - 1];
    if (previous === undefined) return;
    setPageStack((s) => s.slice(0, -1));
    setPageToken(previous);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  function touch(key: string) {
    setTouched((t) => (t.has(key) ? t : new Set(t).add(key)));
  }
  /** The message for `key`, or null while the field is still pristine. */
  function visibleError(key: string, message: string | null): string | null {
    return message !== null && (submitted || touched.has(key)) ? message : null;
  }
  function resetValidation() {
    setTouched(new Set());
    setSubmitted(false);
  }

  function openCreate() {
    resetValidation();
    setDraft(emptyDraft());
  }
  function openEdit(p: Policy) {
    resetValidation();
    setDraft({
      name: p.name,
      subject: p.subject,
      allow: p.allow ? p.allow.map((r) => ({ ...r })) : [],
      deny: p.deny ? p.deny.map((r) => ({ ...r })) : [],
      editing: true,
    });
  }

  async function save() {
    if (!draft) return;
    setSubmitted(true);
    // Every problem is now rendered next to the field that has it.
    if (draftError(draft) !== null) return;
    // env/app are sent as typed: the server normalizes "" and "*" alike to "*"
    // (`policy.normalizeLabel`), so a blank component means "any" and the rule
    // must not be dropped on its way out.
    const clean = (rules: PolicyRule[]) =>
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
      goReset();
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
      await load(pageToken);
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
        hasNext={!!nextToken}
        onNext={goNext}
        hasPrevious={pageStack.length > 0}
        onPrevious={goPrevious}
        onReset={goReset}
        showReset={pageStack.length > 0}
      />

      <Modal
        open={draft !== null}
        wide
        title={draft?.editing ? `Edit policy: ${draft.name}` : "New policy"}
        onClose={() => setDraft(null)}
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
          <>
            <div className="form-row">
              <Field
                label="Name"
                hint={draft.editing ? "Cannot be changed." : "Unique policy name."}
                error={visibleError("name", validatePolicyName(draft.name))}
              >
                <Input
                  className="font-mono"
                  value={draft.name}
                  disabled={draft.editing}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  onBlur={() => touch("name")}
                  placeholder="gradethis-read"
                />
              </Field>
              <Field
                label="Subject"
                hint="The identity this policy applies to, or * for every identity."
                error={visibleError("subject", validatePolicySubject(draft.subject.trim()))}
              >
                <Input
                  className="font-mono"
                  value={draft.subject}
                  onChange={(e) => setDraft({ ...draft, subject: e.target.value })}
                  onBlur={() => touch("subject")}
                  placeholder="gradethis-be"
                />
              </Field>
            </div>

            <div className="info-panel mb-16">
              Deny rules always override allow rules. Environments are application-owned; use
              <span className="mono"> *</span> for either component to match any. A grant covers the
              whole namespace — every key in it — since the namespace is the unit of authorization.
            </div>

            <RuleEditor
              title="Allow rules"
              kind="allow"
              rules={draft.allow}
              onChange={(allow) => setDraft({ ...draft, allow })}
              visibleError={visibleError}
              onTouch={touch}
            />
            <hr className="divider" />
            <RuleEditor
              title="Deny rules"
              kind="deny"
              rules={draft.deny}
              onChange={(deny) => setDraft({ ...draft, deny })}
              visibleError={visibleError}
              onTouch={touch}
            />
          </>
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
  rules: PolicyRule[];
  onChange: (rules: PolicyRule[]) => void;
  visibleError: (key: string, message: string | null) => string | null;
  onTouch: (key: string) => void;
}) {
  const fieldKey = (index: number, field: RuleField) => `${kind}-${index}-${field}`;
  const errorFor = (index: number, rule: PolicyRule, field: RuleField) =>
    visibleError(fieldKey(index, field), ruleFieldError(rule, field));

  function update(index: number, patch: Partial<PolicyRule>) {
    onChange(rules.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  }
  function remove(index: number) {
    onChange(rules.filter((_, i) => i !== index));
  }
  function add() {
    onChange([...rules, newRule()]);
  }

  return (
    <div>
      <div className="between mb-8">
        <div className="field-label" style={{ margin: 0 }}>
          {title}
        </div>
        <Button type="button" variant="outline" size="sm" onClick={add}>
          Add rule
        </Button>
      </div>
      {rules.length === 0 ? (
        <div className="faint text-sm mb-8">No rules.</div>
      ) : (
        <div className="stack">
          {rules.map((rule, i) => (
            <div key={i} className="rule-row" style={{ flexWrap: "wrap" }}>
              <div className="rule-op">
                <Field label="Operation" error={errorFor(i, rule, "operation")}>
                  <AppSelect
                    value={rule.operation}
                    onValueChange={(operation) => update(i, { operation })}
                    onBlur={() => onTouch(fieldKey(i, "operation"))}
                    options={operationOptions(rule.operation).map((operation) => ({
                      value: operation,
                      label: operation,
                    }))}
                  />
                </Field>
              </div>
              <div style={{ width: 130 }}>
                <Field label="App" error={errorFor(i, rule, "app")}>
                  <Input
                    className="font-mono"
                    value={rule.app}
                    onChange={(e) => update(i, { app: e.target.value })}
                    onBlur={() => onTouch(fieldKey(i, "app"))}
                    placeholder="gradethis"
                  />
                </Field>
              </div>
              <div style={{ width: 110 }}>
                <Field label="Env" error={errorFor(i, rule, "env")}>
                  <Input
                    className="font-mono"
                    value={rule.env}
                    onChange={(e) => update(i, { env: e.target.value })}
                    onBlur={() => onTouch(fieldKey(i, "env"))}
                    placeholder="prod"
                  />
                </Field>
              </div>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                className="rule-remove"
                // The row bottom-aligns its cells, and Field carries a bottom
                // margin the bare button does not — this keeps it level.
                style={{ marginBottom: "var(--space-4)" }}
                onClick={() => remove(i)}
                aria-label="Remove rule"
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
