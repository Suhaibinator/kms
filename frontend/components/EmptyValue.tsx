/**
 * A stored value that is the empty string. One component so the wording lives
 * in a single place, and so "empty" is always visibly different from
 * "missing" — a value that exists and is `""` is not a value that is absent.
 */
export function EmptyValue() {
  return (
    <span className="empty-value faint" title="Empty string">
      (empty)
    </span>
  );
}
