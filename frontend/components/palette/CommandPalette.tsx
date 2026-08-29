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
} from "@/lib/palette";
import type { Application } from "@/lib/types";
import { cn } from "@/lib/utils";

export type { CommandPaletteProps };

// The application list is fetched on first open and kept for the session;
// namespaces come from the shared useNamespaces cache. A different token
// (re-login) invalidates the cache.
let cachedApplications: { token: string | null; applications: Application[] } | null = null;

/** Test hook: forget the cached application list. */
export function resetPaletteCache(): void {
  cachedApplications = null;
}

function useApplications(enabled: boolean): Application[] {
  const [applications, setApplications] = useState<Application[]>(
    () => cachedApplications?.applications ?? [],
  );
  useEffect(() => {
    if (!enabled) return;
    const token = getToken();
    if (cachedApplications && cachedApplications.token === token) {
      setApplications(cachedApplications.applications);
      return;
    }
    const controller = new AbortController();
    api
      .listApplications(200, undefined, { signal: controller.signal })
      .then((res) => {
        const list = res.applications ?? [];
        cachedApplications = { token, applications: list };
        setApplications(list);
      })
      .catch((err: unknown) => {
        if (isAbortError(err)) return;
        // The palette still works for pages and environments; nothing to surface.
      });
    return () => controller.abort();
  }, [enabled]);
  return applications;
}

function PaletteBody({ onClose }: { onClose: () => void }) {
  const router = useRouter();
  const { identity } = useAuth();
  const isAdmin = identity?.kind === "admin";
  const applications = useApplications(isAdmin);
  const { namespaces } = useNamespaces();
  const index = useMemo(
    () => buildPaletteIndex({ applications, namespaces, isAdmin }),
    [applications, namespaces, isAdmin],
  );

  // The namespace the operator last worked in (or the one a client identity
  // is bound to) scopes the "Search parameters/secrets for …" fall-throughs.
  const remembered = useLastNamespace();
  const scope = remembered ?? identity?.namespace ?? null;

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
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Reset the highlight whenever the result set changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `results` is the trigger.
  useEffect(() => setActive(0), [results]);

  useEffect(() => {
    const node = listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    node?.scrollIntoView?.({ block: "nearest" });
  }, [active]);

  const navigate = useCallback(
    (item: PaletteItem) => {
      onClose();
      void router.push(item.href);
    },
    [onClose, router],
  );

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (results.length === 0 && event.key !== "Escape") return;
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActive((current) => (current + 1) % results.length);
        break;
      case "ArrowUp":
        event.preventDefault();
        setActive((current) => (current - 1 + results.length) % results.length);
        break;
      case "Home":
        event.preventDefault();
        setActive(0);
        break;
      case "End":
        event.preventDefault();
        setActive(results.length - 1);
        break;
      case "Enter": {
        event.preventDefault();
        const item = results[active];
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
          aria-activedescendant={results.length > 0 ? optionId(active) : undefined}
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
                  {selected ? (
                    <CornerDownLeft size={13} aria-hidden className="palette-item-enter" />
                  ) : null}
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
export default function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const close = useCallback(() => onOpenChange(false), [onOpenChange]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="palette-dialog top-[12dvh] translate-y-0 gap-0 p-0 sm:max-w-xl max-md:inset-0 max-md:top-0 max-md:left-0 max-md:h-full max-md:max-w-none max-md:translate-x-0 max-md:rounded-none"
        aria-describedby={undefined}
      >
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        {open ? <PaletteBody onClose={close} /> : null}
      </DialogContent>
    </Dialog>
  );
}
