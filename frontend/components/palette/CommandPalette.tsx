import { CornerDownLeft, Search } from "lucide-react";
import { useRouter } from "next/router";
import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import type { CommandPaletteProps } from "@/components/applications/contracts";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { Kbd } from "@/components/ui/kbd";
import { useAuth } from "@/context/AuthContext";
import { api, getToken, isAbortError } from "@/lib/api";
import { useNamespaces } from "@/lib/hooks";
import { useLastNamespace } from "@/lib/namespace-memory";
import {
  buildPaletteIndex,
  fallthroughActions,
  groupResults,
  PALETTE_RESULT_LIMIT,
  type PaletteItem,
  rankPalette,
  SHORTCUTS_ACTION_ID,
} from "@/lib/palette";
import type { Application } from "@/lib/types";
import { cn } from "@/lib/utils";

export type { CommandPaletteProps };

// A same-session snapshot makes reopening instant, then every open refreshes
// all pages so application mutations are reflected without mutation plumbing.
let cachedApplications: {
  sessionKey: string;
  applications: Application[];
} | null = null;

/** Test hook: forget the cached application list. */
export function resetPaletteCache(): void {
  cachedApplications = null;
}

function useApplications(enabled: boolean, identityName: string | undefined): Application[] {
  const token = getToken();
  const sessionKey = enabled ? `${token ?? ""}\u0000${identityName ?? ""}` : "";
  const [snapshot, setSnapshot] = useState<{ sessionKey: string; applications: Application[] }>(
    () =>
      cachedApplications?.sessionKey === sessionKey
        ? cachedApplications
        : { sessionKey, applications: [] },
  );
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    let current = true;

    async function refresh() {
      const applications: Application[] = [];
      const seenTokens = new Set<string>();
      let pageToken: string | undefined;
      do {
        const res = await api.listApplications(200, pageToken, { signal: controller.signal });
        applications.push(...(res.applications ?? []));
        pageToken = res.next_page_token || undefined;
        if (pageToken && seenTokens.has(pageToken)) break;
        if (pageToken) seenTokens.add(pageToken);
      } while (pageToken);

      if (!current || getToken() !== token) return;
      const next = { sessionKey, applications };
      cachedApplications = next;
      setSnapshot(next);
    }

    void refresh().catch((err: unknown) => {
      if (isAbortError(err)) return;
      // The palette still works for pages and environments; nothing to surface.
    });
    return () => {
      current = false;
      controller.abort();
    };
  }, [enabled, sessionKey, token]);
  return enabled && snapshot.sessionKey === sessionKey ? snapshot.applications : [];
}

function PaletteBody({ onClose, onShortcuts }: { onClose: () => void; onShortcuts?: () => void }) {
  const router = useRouter();
  const { identity } = useAuth();
  const isAdmin = identity?.kind === "admin";
  const applications = useApplications(isAdmin, identity?.name);
  const { namespaces } = useNamespaces();
  const index = useMemo(
    () => buildPaletteIndex({ applications, namespaces, isAdmin }),
    [applications, namespaces, isAdmin],
  );

  // The namespace the operator last worked in (or the one a client identity
  // is bound to) scopes the "Search parameters/secrets for …" fall-throughs.
  const remembered = useLastNamespace();
  const scope = identity?.kind === "client" ? (identity.namespace ?? null) : remembered;

  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const ranked = useMemo(() => rankPalette(index, query), [index, query]);
  const total = ranked.length;
  const capped = total > PALETTE_RESULT_LIMIT;
  const results = useMemo(
    () => [
      ...(capped ? ranked.slice(0, PALETTE_RESULT_LIMIT) : ranked),
      ...fallthroughActions(query, scope),
    ],
    [ranked, capped, query, scope],
  );
  const groups = useMemo(() => groupResults(results), [results]);
  const orderedResults = useMemo(() => groups.flatMap((group) => group.items), [groups]);
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Reset the highlight whenever the result set changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `orderedResults` is the trigger.
  useEffect(() => setActive(0), [orderedResults]);

  useEffect(() => {
    const node = listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    node?.scrollIntoView?.({ block: "nearest" });
  }, [active]);

  const navigate = useCallback(
    (item: PaletteItem) => {
      onClose();
      // The shortcut sheet is a dialog the shell owns, not a route.
      if (item.id === SHORTCUTS_ACTION_ID) {
        onShortcuts?.();
        return;
      }
      void router.push(item.href);
    },
    [onClose, onShortcuts, router],
  );

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (orderedResults.length === 0 && event.key !== "Escape") return;
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActive((current) => (current + 1) % orderedResults.length);
        break;
      case "ArrowUp":
        event.preventDefault();
        setActive((current) => (current - 1 + orderedResults.length) % orderedResults.length);
        break;
      case "Home":
        event.preventDefault();
        setActive(0);
        break;
      case "End":
        event.preventDefault();
        setActive(orderedResults.length - 1);
        break;
      case "Enter": {
        event.preventDefault();
        const item = orderedResults[active];
        if (item) navigate(item);
        break;
      }
      case "Escape":
        event.preventDefault();
        onClose();
        break;
      default:
        break;
    }
  };

  const optionId = (position: number) => `${listId}-option-${position}`;
  const groupLabelId = (group: string) => `${listId}-group-${group.toLowerCase()}`;
  let position = -1;

  return (
    <div className="palette">
      <div className="palette-input-row">
        <Search size={16} strokeWidth={1.9} aria-hidden className="palette-input-icon" />
        <input
          ref={inputRef}
          className="palette-input"
          type="text"
          role="combobox"
          aria-label="Search applications, environments, pages and actions"
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={orderedResults.length > 0 ? optionId(active) : undefined}
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder="Search or jump to…"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={onKeyDown}
        />
        <Kbd className="palette-esc">esc</Kbd>
      </div>
      <div
        ref={listRef}
        id={listId}
        role="listbox"
        aria-label="Results"
        className="palette-list"
        onMouseDown={(event) => event.preventDefault()}
      >
        {total === 0 ? (
          <div className="palette-empty">
            No matches for <span className="mono">{query.trim()}</span>.
          </div>
        ) : null}
        {groups.map((group) => (
          // biome-ignore lint/a11y/useSemanticElements: a listbox owns options and ARIA groups of options; a fieldset has no place inside it.
          <div
            key={group.group}
            className="palette-group"
            role="group"
            aria-labelledby={groupLabelId(group.group)}
          >
            <div id={groupLabelId(group.group)} className="palette-group-label">
              {group.group}
            </div>
            {group.items.map((item) => {
              position += 1;
              const mine = position;
              const selected = mine === active;
              return (
                <div
                  key={item.id}
                  id={optionId(mine)}
                  role="option"
                  tabIndex={-1}
                  aria-selected={selected}
                  data-index={mine}
                  data-item={item.id}
                  className={cn("palette-item", selected && "palette-item-active")}
                  onMouseEnter={() => setActive(mine)}
                  onClick={() => navigate(item)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") navigate(item);
                  }}
                >
                  <span className="palette-item-title">{item.title}</span>
                  {item.subtitle ? <span className="palette-item-sub">{item.subtitle}</span> : null}
                  {/* Mounted on every row and hidden with visibility: the glyph
                      is flex-shrink: 0, so mounting it only on the selected row
                      re-truncated that row's subtitle on every arrow key. */}
                  <CornerDownLeft
                    size={13}
                    aria-hidden
                    className={cn("palette-item-enter", !selected && "palette-item-enter-idle")}
                  />
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {capped ? (
        <div className="palette-empty palette-more" data-testid="palette-more">
          {PALETTE_RESULT_LIMIT} of {total}{" "}
          {query.trim() ? "matches — keep typing" : "— type to narrow"}
        </div>
      ) : null}
      <div className="palette-foot">
        <span>
          <Kbd>↑</Kbd>
          <Kbd>↓</Kbd> navigate
        </span>
        <span>
          <Kbd>↵</Kbd> open
        </span>
        <span>
          <Kbd>esc</Kbd> close
        </span>
      </div>
    </div>
  );
}

/**
 * ⌘K command palette on Base UI's Dialog: a combobox over a grouped listbox.
 * The body mounts only while open, so the index is built (and applications
 * fetched) on first use rather than on every page.
 */
export default function CommandPalette({ open, onOpenChange, onShortcuts }: CommandPaletteProps) {
  const close = useCallback(() => onOpenChange(false), [onOpenChange]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="palette-dialog top-[12dvh] translate-y-0 gap-0 p-0 sm:max-w-xl max-md:inset-0 max-md:top-0 max-md:left-0 max-md:h-full max-md:max-w-none max-md:translate-x-0 max-md:rounded-none"
        aria-describedby={undefined}
      >
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        {open ? <PaletteBody onClose={close} onShortcuts={onShortcuts} /> : null}
      </DialogContent>
    </Dialog>
  );
}
