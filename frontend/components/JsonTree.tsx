import { ChevronDown, ChevronRight } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { type TokenKind, tokenizeJson } from "@/lib/json-text";
import { cn } from "@/lib/utils";

/**
 * A collapsible read-only view of a JSON document.
 *
 * The document is parsed from the raw text (never from `JSON.parse`, so
 * `1.0`, big integers and string escapes survive verbatim) and pretty-printed
 * here with two-space indentation, which gives every node a known line. That
 * is what lets a collapsed subtree keep the line numbers of the nodes still on
 * screen: the numbers jump, and the jump is the signal that lines are hidden.
 */

/** An object or array whose direct children outnumber this starts collapsed. */
export const AUTO_COLLAPSE_CHILDREN = 200;

interface ScalarNode {
  kind: "scalar";
  token: TokenKind;
  /** The source slice, unchanged. */
  text: string;
  span: 1;
}

interface ContainerNode {
  kind: "object" | "array";
  entries: JsonTreeEntry[];
  /** Lines this node occupies when everything under it is expanded. */
  span: number;
}

export type JsonTreeNode = ScalarNode | ContainerNode;

interface JsonTreeEntry {
  /** The quoted property name, or null inside an array. */
  key: string | null;
  node: JsonTreeNode;
}

/**
 * Builds the node tree for well-formed JSON, or null for anything else. The
 * walk is iterative, so a pathologically deep document cannot overflow the
 * stack.
 */
export function parseJsonTree(text: string): JsonTreeNode | null {
  const stack: ContainerNode[] = [];
  const pendingKeys: (string | null)[] = [];
  // A box rather than a plain `let`: the assignment happens inside `attach`,
  // and TypeScript does not track narrowing across that boundary.
  const root: { node: JsonTreeNode | null } = { node: null };
  const attach = (node: JsonTreeNode): boolean => {
    if (stack.length === 0) {
      if (root.node !== null) return false;
      root.node = node;
      return true;
    }
    const depth = stack.length - 1;
    stack[depth].entries.push({ key: pendingKeys[depth], node });
    pendingKeys[depth] = null;
    return true;
  };
  for (const token of tokenizeJson(text)) {
    if (token.kind === "ws") continue;
    if (token.kind === "error") return null;
    const raw = text.slice(token.start, token.end);
    if (token.kind === "key") {
      if (stack.length === 0) return null;
      pendingKeys[stack.length - 1] = raw;
      continue;
    }
    if (token.kind === "punct") {
      if (raw === "{" || raw === "[") {
        const node: ContainerNode = {
          kind: raw === "{" ? "object" : "array",
          entries: [],
          span: 1,
        };
        if (!attach(node)) return null;
        stack.push(node);
        pendingKeys.push(null);
        continue;
      }
      if (raw === "}" || raw === "]") {
        const node = stack.pop();
        pendingKeys.pop();
        if (!node) return null;
        // One line for the opener, one for the closer; an empty container is
        // printed as `{}` on a single line.
        node.span =
          node.entries.length === 0
            ? 1
            : 2 + node.entries.reduce((total, entry) => total + entry.node.span, 0);
        continue;
      }
      // `,` and `:` carry no structure of their own.
      continue;
    }
    if (!attach({ kind: "scalar", token: token.kind, text: raw, span: 1 })) return null;
  }
  return stack.length === 0 ? root.node : null;
}

function isContainer(node: JsonTreeNode): node is ContainerNode {
  return node.kind === "object" || node.kind === "array";
}

/** A property name without its quotes, for the path shown to assistive tech. */
function unquote(raw: string): string {
  try {
    const value = JSON.parse(raw);
    return typeof value === "string" ? value : raw;
  } catch {
    return raw;
  }
}

function childPath(path: string, key: string | null, index: number): string {
  if (key === null) return `${path}[${index}]`;
  const name = unquote(key);
  return path === "" ? name : `${path}.${name}`;
}

interface Toggle {
  id: string;
  /** Dotted path, empty at the root. */
  path: string;
  collapsed: boolean;
}

export interface JsonTreeRow {
  /** Structural path (`0.2.1`), unique whatever the property names are. */
  id: string;
  /** 1-based line in the fully expanded document. */
  line: number;
  depth: number;
  /** The quoted property name, when this row names one. */
  key: string | null;
  /** A scalar, an empty container (`{}`), or a bracket on its own line. */
  value: { token: TokenKind; text: string } | null;
  /** `{ … 12 keys }` in place of a collapsed subtree. */
  summary: { open: string; close: string; label: string } | null;
  toggle: Toggle | null;
  comma: boolean;
}

type Work =
  | {
      step: "node";
      node: JsonTreeNode;
      id: string;
      path: string;
      key: string | null;
      depth: number;
      comma: boolean;
    }
  | { step: "close"; text: string; depth: number; comma: boolean };

function summaryLabel(node: ContainerNode): string {
  const count = node.entries.length;
  if (node.kind === "object") return `… ${count} ${count === 1 ? "key" : "keys"}`;
  return `… ${count} ${count === 1 ? "item" : "items"}`;
}

/** The visible lines, in order, with the line numbers of the expanded document. */
export function buildJsonTreeRows(
  root: JsonTreeNode | null,
  collapsed: ReadonlySet<string>,
): JsonTreeRow[] {
  if (!root) return [];
  const rows: JsonTreeRow[] = [];
  const stack: Work[] = [
    { step: "node", node: root, id: "$", path: "", key: null, depth: 0, comma: false },
  ];
  let line = 1;
  while (stack.length > 0) {
    const item = stack.pop();
    if (!item) break;
    if (item.step === "close") {
      rows.push({
        id: `${item.text}${line}`,
        line,
        depth: item.depth,
        key: null,
        value: { token: "punct", text: item.text },
        summary: null,
        toggle: null,
        comma: item.comma,
      });
      line += 1;
      continue;
    }
    const { node } = item;
    const base = {
      id: item.id,
      line,
      depth: item.depth,
      key: item.key,
      comma: item.comma,
    };
    if (!isContainer(node)) {
      rows.push({
        ...base,
        value: { token: node.token, text: node.text },
        summary: null,
        toggle: null,
      });
      line += 1;
      continue;
    }
    const open = node.kind === "object" ? "{" : "[";
    const close = node.kind === "object" ? "}" : "]";
    if (node.entries.length === 0) {
      rows.push({
        ...base,
        value: { token: "punct", text: `${open}${close}` },
        summary: null,
        toggle: null,
      });
      line += 1;
      continue;
    }
    if (collapsed.has(item.id)) {
      rows.push({
        ...base,
        value: null,
        summary: { open, close, label: summaryLabel(node) },
        toggle: { id: item.id, path: item.path, collapsed: true },
      });
      // Every line of the subtree is hidden, so the next visible row keeps its
      // own number and the gap shows what is folded away.
      line += node.span;
      continue;
    }
    rows.push({
      ...base,
      // The separating comma belongs to the closing bracket's line.
      comma: false,
      value: { token: "punct", text: open },
      summary: null,
      toggle: { id: item.id, path: item.path, collapsed: false },
    });
    line += 1;
    stack.push({ step: "close", text: close, depth: item.depth, comma: item.comma });
    for (let index = node.entries.length - 1; index >= 0; index -= 1) {
      const entry = node.entries[index];
      stack.push({
        step: "node",
        node: entry.node,
        id: `${item.id}.${index}`,
        path: childPath(item.path, entry.key, index),
        key: entry.key,
        depth: item.depth + 1,
        comma: index < node.entries.length - 1,
      });
    }
  }
  return rows;
}

/** Ids of every container that holds something, in document order. */
function collectContainers(root: JsonTreeNode | null, minEntries = 1): string[] {
  if (!root) return [];
  const ids: string[] = [];
  const stack: Array<{ node: JsonTreeNode; id: string }> = [{ node: root, id: "$" }];
  while (stack.length > 0) {
    const item = stack.pop();
    if (!item) break;
    const { node, id } = item;
    if (!isContainer(node)) continue;
    if (node.entries.length >= minEntries) ids.push(id);
    for (let index = node.entries.length - 1; index >= 0; index -= 1) {
      stack.push({ node: node.entries[index].node, id: `${id}.${index}` });
    }
  }
  return ids;
}

const NO_COLLAPSE: ReadonlySet<string> = new Set<string>();

function autoCollapsed(root: JsonTreeNode | null): ReadonlySet<string> {
  const ids = collectContainers(root, AUTO_COLLAPSE_CHILDREN + 1);
  return ids.length === 0 ? NO_COLLAPSE : new Set(ids);
}

export interface JsonTreeState {
  /** Null when the text is not JSON the tree can render. */
  tree: JsonTreeNode | null;
  collapsed: ReadonlySet<string>;
  toggle: (id: string) => void;
  expandAll: () => void;
  collapseAll: () => void;
  /** True while at least one node can still be collapsed. */
  canCollapse: boolean;
  /** True while at least one node is collapsed. */
  canExpand: boolean;
}

/**
 * Parses `raw` and holds the per-node collapse state, keyed by structural
 * path. The state resets whenever the text changes; everything starts expanded
 * except containers over {@link AUTO_COLLAPSE_CHILDREN} direct children.
 */
export function useJsonTree(raw: string, enabled: boolean): JsonTreeState {
  const tree = useMemo(() => {
    if (!enabled) return null;
    const parsed = parseJsonTree(raw);
    // A scalar or an empty container has nothing to fold: leave those to the
    // plain highlighted block rather than showing a dead toolbar.
    if (!parsed || !isContainer(parsed) || parsed.entries.length === 0) return null;
    return parsed;
  }, [enabled, raw]);
  const containers = useMemo(() => collectContainers(tree), [tree]);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => autoCollapsed(tree));
  const [source, setSource] = useState<JsonTreeNode | null>(tree);
  if (source !== tree) {
    // A new document: drop collapse state that belonged to the old one.
    setSource(tree);
    setCollapsed(autoCollapsed(tree));
  }
  const toggle = useCallback((id: string) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }, []);
  const expandAll = useCallback(() => setCollapsed(NO_COLLAPSE), []);
  const collapseAll = useCallback(() => setCollapsed(new Set(containers)), [containers]);
  return {
    tree,
    collapsed,
    toggle,
    expandAll,
    collapseAll,
    canCollapse: containers.some((id) => !collapsed.has(id)),
    canExpand: collapsed.size > 0,
  };
}

function Caret({ toggle, onToggle }: { toggle: Toggle; onToggle: (id: string) => void }) {
  const verb = toggle.collapsed ? "Expand" : "Collapse";
  const Icon = toggle.collapsed ? ChevronRight : ChevronDown;
  return (
    <button
      type="button"
      className="json-tree-caret"
      aria-expanded={!toggle.collapsed}
      aria-label={`${verb} ${toggle.path === "" ? "root" : toggle.path}`}
      onClick={() => onToggle(toggle.id)}
    >
      <Icon size={12} aria-hidden />
    </button>
  );
}

/**
 * One printed line. The line number is a `::before` fed by `data-line`, so it
 * is never part of `textContent` and never lands in a text selection.
 */
function TreeLine({
  row,
  last,
  lineNumbers,
  onToggle,
}: {
  row: JsonTreeRow;
  last: boolean;
  lineNumbers: boolean;
  onToggle: (id: string) => void;
}) {
  return (
    <span className="json-tree-line" data-line={lineNumbers ? row.line : undefined}>
      {row.toggle ? (
        <Caret toggle={row.toggle} onToggle={onToggle} />
      ) : (
        <span className="json-tree-caret-slot" />
      )}
      <span className="json-tree-code">
        {"  ".repeat(row.depth)}
        {row.key !== null ? (
          <>
            <span className="tok-key">{row.key}</span>
            <span className="tok-punct">:</span>{" "}
          </>
        ) : null}
        {row.value ? <span className={`tok-${row.value.token}`}>{row.value.text}</span> : null}
        {row.summary ? (
          <>
            <span className="tok-punct">{row.summary.open}</span>
            <span className="json-tree-summary">{` ${row.summary.label} `}</span>
            <span className="tok-punct">{row.summary.close}</span>
          </>
        ) : null}
        {row.comma ? <span className="tok-punct">,</span> : null}
        {last ? null : "\n"}
      </span>
    </span>
  );
}

export interface JsonTreeProps {
  state: JsonTreeState;
  /** Number the visible lines with their line in the expanded document. */
  lineNumbers?: boolean;
  className?: string;
}

/** Renders the tree held by {@link useJsonTree}; nothing when it has no document. */
export function JsonTree({ state, lineNumbers = false, className }: JsonTreeProps) {
  const { tree, collapsed, toggle } = state;
  const rows = useMemo(() => buildJsonTreeRows(tree, collapsed), [tree, collapsed]);
  // Sized from the whole document, not the visible rows, so folding a subtree
  // never shifts the code sideways.
  const gutterChars = String(tree ? tree.span : 1).length;
  if (!tree) return null;
  return (
    <span
      className={cn("json-tree", className)}
      data-line-numbers={lineNumbers ? "true" : undefined}
      style={
        lineNumbers
          ? ({ "--json-gutter-chars": String(gutterChars) } as React.CSSProperties)
          : undefined
      }
    >
      {rows.map((row, index) => (
        <TreeLine
          // Rows have no identity beyond their position.
          key={index}
          row={row}
          last={index === rows.length - 1}
          lineNumbers={lineNumbers}
          onToggle={toggle}
        />
      ))}
    </span>
  );
}
