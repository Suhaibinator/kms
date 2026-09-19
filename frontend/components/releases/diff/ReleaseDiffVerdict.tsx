import { countNoun } from "@/lib/format";
import {
  type FieldAggregate,
  formatFieldCounts,
  rolloutSentence,
  rolloutTone,
} from "@/lib/release-diff";
import type { OverviewRollout, ReleaseDiffResponse } from "@/lib/types";

type FactTone = "added" | "removed" | "changed" | "warning" | "danger" | "success";

interface Fact {
  /** Suffix of the `release-diff-` testid. */
  id: string;
  text: string;
  tone?: FactTone;
  /** A count of nothing or a "same" statement: rendered faint. */
  zero?: boolean;
}

function facts(
  diff: ReleaseDiffResponse,
  fields: FieldAggregate | null,
  rollout: OverviewRollout | null | undefined,
): Fact[] {
  const { counts, from, to } = diff;
  const out: Fact[] = [];
  if (!diff.identical) {
    // `counts.changed` includes repinned secrets, so the noun widens when any are in it.
    const noun = counts.secrets_changed > 0 ? "entries" : "parameters";
    out.push({
      id: "count-changed",
      text: `${counts.changed} ${countNoun(counts.changed, noun)} changed`,
      tone: "changed",
      zero: counts.changed === 0,
    });
    out.push({
      id: "count-added",
      text: `${counts.added} added`,
      tone: "added",
      zero: counts.added === 0,
    });
    out.push({
      id: "count-removed",
      text: `${counts.removed} removed`,
      tone: "removed",
      zero: counts.removed === 0,
    });
    if (fields && fields.total > 0) {
      const total = `${fields.total}${fields.partial ? "+" : ""}`;
      out.push({
        id: "fields-total",
        text: `${total} ${countNoun(fields.total, "fields")} (${formatFieldCounts(fields.counts)})`,
      });
    }
  }
  const secrets = counts.secrets_changed;
  out.push(
    secrets === 0
      ? { id: "count-secrets", text: "no secrets repinned", zero: true }
      : {
          id: "count-secrets",
          text: `${secrets} ${countNoun(secrets, "secrets")} repinned`,
          tone: "warning",
        },
  );
  out.push(
    diff.schema_changed
      ? {
          id: "schema",
          text: `schema v${from.schema_version} → v${to.schema_version}, different tracks`,
          tone: "warning",
        }
      : { id: "schema", text: `schema v${from.schema_version} unchanged`, zero: true },
  );
  if (!diff.values_included) {
    out.push({ id: "values", text: "entries only, values not requested" });
  }
  if (rollout !== undefined) {
    const tone = rolloutTone(rollout);
    out.push({
      id: "rollout",
      text: rolloutSentence(rollout),
      tone: tone === "success" || tone === "danger" ? tone : undefined,
      zero: tone === "zero",
    });
  }
  return out;
}

/**
 * The comparison in one line of `·`-separated facts: what changed, at what
 * scale, whether secrets or the schema moved, and how the rollout is going.
 * Facts that say "nothing" are faint; the separators are CSS, so every
 * fact's text stays exact for the tests and the clipboard.
 */
export function ReleaseDiffVerdict({
  diff,
  fields,
  rollout,
}: {
  diff: ReleaseDiffResponse;
  fields: FieldAggregate | null;
  /** undefined hides the rollout fact; null renders it as unknown. */
  rollout?: OverviewRollout | null;
}) {
  return (
    <p className="release-diff-verdict" data-testid="release-diff-strip">
      {facts(diff, fields, rollout).map((fact) => (
        <span
          key={fact.id}
          className="release-diff-verdict-fact"
          data-zero={fact.zero ? "true" : undefined}
          data-tone={fact.zero ? undefined : fact.tone}
          data-testid={`release-diff-${fact.id}`}
        >
          {fact.text}
        </span>
      ))}
    </p>
  );
}
