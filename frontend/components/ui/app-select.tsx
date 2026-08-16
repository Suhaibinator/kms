import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type { FocusEventHandler } from "react";

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
  disabled,
  required,
  name,
  id,
  className,
  onBlur,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
}: {
  value: string;
  onValueChange: (value: string) => void;
  options: AppSelectOption[];
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
  name?: string;
  id?: string;
  className?: string;
  onBlur?: FocusEventHandler<HTMLButtonElement>;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  const items = options.map(({ value: optionValue, label }) => ({
    value: optionValue,
    label,
  }));

  return (
    <Select
      value={value || null}
      onValueChange={(next) => onValueChange(next ?? "")}
      items={items}
      disabled={disabled}
      required={required}
      name={name}
    >
      <SelectTrigger
        id={id}
        className={cn("w-full", className)}
        onBlur={onBlur}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
      >
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent align="start">
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
