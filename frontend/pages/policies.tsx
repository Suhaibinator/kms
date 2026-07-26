import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { POLICY_OPERATIONS, type Policy, type PolicyRule } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs } from "@/lib/format";
import {
  EmptyState,
  Field,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";

interface Draft {
  name: string;
  subject: string;
  allow: PolicyRule[];
  deny: PolicyRule[];
  editing: boolean;
}

function emptyDraft(): Draft {
  return { name: "", subject: "", allow: [], deny: [], editing: false };
}

function newRule(): PolicyRule {
  return { operation: "secret:read", env: "*", app: "*" };
}

export default function PoliciesPage() {
  const toast = useToast();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(
    async (token: string) => {
      setLoading(true);
      try {
        const res = await api.listPolicies(100, token || undefined);
        setPolicies(res.policies ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load policies");
      } finally {
        setLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken);
  }, [load, pageToken]);

  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  function openCreate() {
    setDraft(emptyDraft());
  }
  function openEdit(p: Policy) {
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
    if (!draft.name.trim()) {
      toast.error(new Error("A policy name is required."), "Missing name");
      return;
    }
    if (!draft.subject.trim()) {
      toast.error(new Error("A subject is required."), "Missing subject");
      return;
    }
    // Drop incomplete rules; env/app are both required (use "*" for any).
    const clean = (rules: PolicyRule[]) =>
      rules
        .filter((r) => r.env.trim() !== "" && r.app.trim() !== "")
        .map((r) => ({
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
        actions={
          <button className="btn btn-primary" onClick={openCreate}>
            New policy
          </button>
        }
      />

      {loading ? (
        <TableSkeleton
          headers={["Name", "Subject", "Allow", "Deny", "Updated"]}
        />
      ) : policies.length === 0 ? (
        <EmptyState
          icon={<Icon.policy size={20} />}
          title="No policies yet"
          actions={
            <button className="btn btn-primary" onClick={openCreate}>
              New policy
            </button>
          }
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
                      <button className="btn btn-sm" onClick={() => openEdit(p)}>
                        Edit
                      </button>
                      <button
                        className="btn btn-sm btn-danger"
                        onClick={() => setDeleteTarget(p.name)}
                      >
                        Delete
                      </button>
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
            <button className="btn" onClick={() => setDraft(null)} disabled={saving}>
              Cancel
            </button>
            <button className="btn btn-primary" onClick={save} disabled={saving}>
              {saving ? <Spinner /> : null}
              {draft?.editing ? "Save changes" : "Create policy"}
            </button>
          </>
        }
      >
        {draft ? (
          <>
            <div className="form-row">
              <Field label="Name" hint={draft.editing ? "Cannot be changed." : "Unique policy name."}>
                <input
                  className="input mono"
                  value={draft.name}
                  disabled={draft.editing}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder="gradethis-read"
                />
              </Field>
              <Field label="Subject" hint="The identity this policy applies to.">
                <input
                  className="input mono"
                  value={draft.subject}
                  onChange={(e) => setDraft({ ...draft, subject: e.target.value })}
                  placeholder="gradethis-be"
                />
              </Field>
            </div>

            <div className="info-panel mb-16">
              Deny rules always override allow rules. Use <span className="mono">*</span> for env or
              app to match any. A grant covers the whole namespace — every key in it — since the
              namespace is the unit of authorization.
            </div>

            <RuleEditor
              title="Allow rules"
              rules={draft.allow}
              onChange={(allow) => setDraft({ ...draft, allow })}
            />
            <hr className="divider" />
            <RuleEditor
              title="Deny rules"
              rules={draft.deny}
              onChange={(deny) => setDraft({ ...draft, deny })}
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
  rules,
  onChange,
}: {
  title: string;
  rules: PolicyRule[];
  onChange: (rules: PolicyRule[]) => void;
}) {
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
        <button type="button" className="btn btn-sm" onClick={add}>
          Add rule
        </button>
      </div>
      {rules.length === 0 ? (
        <div className="faint text-sm mb-8">No rules.</div>
      ) : (
        <div className="stack">
          {rules.map((rule, i) => (
            <div key={i} className="rule-row" style={{ flexWrap: "wrap" }}>
              <div className="rule-op">
                <label className="field-label">Operation</label>
                <select
                  className="select"
                  value={rule.operation}
                  onChange={(e) => update(i, { operation: e.target.value })}
                >
                  {POLICY_OPERATIONS.map((op) => (
                    <option key={op} value={op}>
                      {op}
                    </option>
                  ))}
                </select>
              </div>
              <div style={{ width: 110 }}>
                <label className="field-label">Env</label>
                <input
                  className="input mono"
                  value={rule.env}
                  onChange={(e) => update(i, { env: e.target.value })}
                  placeholder="prod"
                />
              </div>
              <div style={{ width: 130 }}>
                <label className="field-label">App</label>
                <input
                  className="input mono"
                  value={rule.app}
                  onChange={(e) => update(i, { app: e.target.value })}
                  placeholder="gradethis"
                />
              </div>
              <button
                type="button"
                className="btn btn-sm btn-danger rule-remove"
                onClick={() => remove(i)}
                aria-label="Remove rule"
              >
                Remove
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
