import { JsonHighlight } from "@/components/JsonHighlight";
import { checkJson, overHighlightCap } from "@/lib/json-text";

/** A read-only JSON block; colour-coded when the text is well-formed JSON. */
export function JsonView({ raw, highlight = true }: { raw: string; highlight?: boolean }) {
  const coloured = highlight && !overHighlightCap(raw) && checkJson(raw) === null;
  return <pre className="json-block">{coloured ? <JsonHighlight text={raw} /> : raw}</pre>;
}
