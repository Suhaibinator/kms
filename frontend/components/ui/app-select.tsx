import type { FocusEventHandler, Ref } from "react";
import { SearchableAppSelect } from "@/components/ui/searchable-app-select";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface AppSelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export function AppSelect({
  value,
  onValueChange,
  options,
  placeholder = "Select…",
  emptyOptionLabel,
  disabled,
  required,
  name,
  id,
  className,
  searchable = false,
  searchPlaceholder = "Filter options…",
  emptyMessage = "No matching options.",
  onBlur,
  ref,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-label": ariaLabel,
}: {
  value: string;
  onValueChange: (value: string) => void;
  options: AppSelectOption[];
  placeholder?: string;
  /** Adds an explicit option that calls `onValueChange("")`, allowing a selection to be reset. */
  emptyOptionLabel?: string;
  disabled?: boolean;
  required?: boolean;
  name?: string;
  id?: string;
  className?: string;
  searchable?: boolean;
  searchPlaceholder?: string;
  emptyMessage?: string;
  onBlur?: FocusEventHandler<HTMLButtonElement>;
  /** The trigger button, e.g. for a modal's `initialFocus`. */
  ref?: Ref<HTMLButtonElement>;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-label"?: string;
}) {
  const effectivelyDisabled = disabled || (options.length === 0 && !emptyOptionLabel);
  let emptyOptionValue = "__app-select-empty-option__";
  while (options.some((option) => option.value === emptyOptionValue)) emptyOptionValue += "_";

  if (searchable) {
    const searchableOptions = [
      ...(emptyOptionLabel ? [{ value: emptyOptionValue, label: emptyOptionLabel }] : []),
      ...options,
    ];
    return (
      <SearchableAppSelect
        value={value}
        onValueChange={(next) => onValueChange(next === emptyOptionValue ? "" : next)}
        options={searchableOptions}
        placeholder={placeholder}
        searchPlaceholder={searchPlaceholder}
        emptyMessage={emptyMessage}
        disabled={effectivelyDisabled}
        required={required}
        name={name}
        id={id}
        className={className}
        onBlur={onBlur}
        ref={ref}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
        aria-label={ariaLabel}
      />
    );
  }

  const items = [
    ...(emptyOptionLabel ? [{ value: emptyOptionValue, label: emptyOptionLabel }] : []),
    ...options.map(({ value: optionValue, label }) => ({ value: optionValue, label })),
  ];

  return (
    <Select
      value={value || null}
      onValueChange={(next) =>
        onValueChange(next === emptyOptionValue || next === null ? "" : next)
      }
      items={items}
      disabled={effectivelyDisabled}
      required={required}
      name={name}
    >
      <SelectTrigger
        ref={ref}
        id={id}
        className={cn("w-full", className)}
        onBlur={onBlur}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
        aria-label={ariaLabel}
      >
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent align="start">
        {emptyOptionLabel ? (
          <SelectItem value={emptyOptionValue}>{emptyOptionLabel}</SelectItem>
        ) : null}
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
