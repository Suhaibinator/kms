import { Monitor, Moon, Sun } from "lucide-react";
import { useId } from "react";
import { type ThemePreference, useTheme } from "@/lib/theme";

const OPTIONS: Array<{ value: ThemePreference; label: string; Icon: typeof Sun }> = [
  { value: "system", label: "Match system", Icon: Monitor },
  { value: "light", label: "Light", Icon: Sun },
  { value: "dark", label: "Dark", Icon: Moon },
];

/** Three-way theme control: follow the OS, or pin light / dark for this browser. */
export function ThemeSwitch() {
  const { preference, setPreference } = useTheme();
  // Native radios give arrow-key navigation and form semantics for free; the
  // input itself is visually hidden and the label carries the icon.
  const name = useId();
  return (
    <fieldset className="theme-switch">
      <legend className="sr-only">Theme</legend>
      {OPTIONS.map(({ value, label, Icon }) => (
        <label key={value} className="theme-switch-option" title={label}>
          <input
            type="radio"
            name={name}
            value={value}
            className="sr-only"
            aria-label={label}
            checked={preference === value}
            onChange={() => setPreference(value)}
          />
          <Icon size={14} aria-hidden />
        </label>
      ))}
    </fieldset>
  );
}
