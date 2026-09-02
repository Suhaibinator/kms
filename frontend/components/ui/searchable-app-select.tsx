import { Combobox } from "@base-ui/react/combobox";
import { CheckIcon, ChevronDownIcon, SearchIcon } from "lucide-react";
import type { FocusEventHandler, Ref } from "react";
import { cn } from "@/lib/utils";

export interface SearchableAppSelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export function SearchableAppSelect({
  value,
  onValueChange,
  options,
  placeholder,
  searchPlaceholder,
  emptyMessage,
  disabled,
  required,
  name,
  id,
  className,
  onBlur,
  ref,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
}: {
  value: string;
  onValueChange: (value: string) => void;
  options: SearchableAppSelectOption[];
  placeholder: string;
  searchPlaceholder: string;
  emptyMessage: string;
  disabled?: boolean;
  required?: boolean;
  name?: string;
  id?: string;
  className?: string;
  onBlur?: FocusEventHandler<HTMLButtonElement>;
  ref?: Ref<HTMLButtonElement>;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  const selected = options.find((option) => option.value === value) ?? null;

  return (
    <Combobox.Root
      value={selected}
      onValueChange={(option) => onValueChange(option?.value ?? "")}
      items={options}
      itemToStringLabel={(option) => option.label}
      itemToStringValue={(option) => option.value}
      isItemEqualToValue={(option, current) => option.value === current.value}
      autoHighlight
      disabled={disabled}
      required={required}
      name={name}
    >
      <Combobox.Trigger
        ref={ref}
        id={id}
        data-slot="select-trigger"
        className={cn(
          "group/select-trigger flex h-(--control-h) w-full items-center justify-between gap-2 rounded-md border border-input bg-input/20 py-2 pr-2 pl-3 text-sm whitespace-nowrap shadow-[inset_0_1px_rgba(255,255,255,0.025)] transition-[border-color,background-color,box-shadow] outline-none select-none hover:border-foreground/25 hover:bg-input/30 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/35 aria-expanded:border-primary/60 aria-expanded:bg-input/35 aria-expanded:ring-3 aria-expanded:ring-primary/10 disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 data-placeholder:text-muted-foreground dark:disabled:bg-input/80 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
          className,
        )}
        onBlur={onBlur}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
      >
        <Combobox.Value>
          {(option: SearchableAppSelectOption | null) => (
            <span className="min-w-0 flex-1 truncate text-left font-medium tracking-[-0.005em]">
              {option?.label ?? placeholder}
            </span>
          )}
        </Combobox.Value>
        <Combobox.Icon className="flex h-6 items-center border-l border-border/80 pl-2 text-muted-foreground transition-colors group-hover/select-trigger:text-foreground group-aria-expanded/select-trigger:text-primary">
          <ChevronDownIcon className="size-3.5 transition-transform duration-150 group-aria-expanded/select-trigger:rotate-180" />
        </Combobox.Icon>
      </Combobox.Trigger>

      <Combobox.Portal>
        <Combobox.Positioner side="bottom" sideOffset={6} align="start" className="isolate z-50">
          <Combobox.Popup
            data-slot="combobox-content"
            aria-label={`${searchPlaceholder} options`}
            className="relative isolate z-50 flex max-h-(--available-height) w-(--anchor-width) min-w-56 origin-(--transform-origin) flex-col overflow-hidden rounded-lg border border-border/90 bg-popover/98 font-sans text-popover-foreground shadow-(--shadow) ring-1 ring-border outline-none backdrop-blur-md duration-100 data-[side=bottom]:slide-in-from-top-1 data-[side=top]:slide-in-from-bottom-1 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-98 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-98"
          >
            <div className="flex shrink-0 items-center gap-2 border-b border-border/80 px-2">
              <SearchIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
              <Combobox.Input
                aria-label={searchPlaceholder}
                placeholder={searchPlaceholder}
                className="h-10 min-w-0 flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground"
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-1">
              <Combobox.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
                {emptyMessage}
              </Combobox.Empty>
              <Combobox.List className="space-y-0.5">
                {(option: SearchableAppSelectOption) => (
                  <Combobox.Item
                    key={option.value}
                    value={option}
                    disabled={option.disabled}
                    data-slot="combobox-item"
                    className="relative flex min-h-9 w-full cursor-default items-center gap-2 overflow-hidden rounded-md py-2 pr-9 pl-3 text-sm text-muted-foreground outline-hidden select-none before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-primary before:opacity-0 before:transition-opacity aria-selected:bg-primary/10 aria-selected:font-semibold aria-selected:text-foreground aria-selected:before:opacity-100 data-highlighted:bg-accent data-highlighted:text-accent-foreground data-highlighted:before:opacity-100 data-disabled:pointer-events-none data-disabled:opacity-45"
                  >
                    <span className="min-w-0 flex-1 truncate">{option.label}</span>
                    <Combobox.ItemIndicator className="pointer-events-none absolute right-2 flex size-5 items-center justify-center rounded-full bg-primary/15 text-primary">
                      <CheckIcon className="size-3.5" aria-hidden />
                    </Combobox.ItemIndicator>
                  </Combobox.Item>
                )}
              </Combobox.List>
            </div>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  );
}
