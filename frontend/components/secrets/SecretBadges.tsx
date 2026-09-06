import { Badge } from "@/components/ui";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * The one vocabulary for a secret's protection, wherever it is shown as state:
 * "binding key" (clients present a key that participates in decryption) or
 * "master key only". "Bind" / "Unbind" / "Rotate key" stay verbs on buttons.
 * Both are classifications, not problems, so neither uses the warning tone.
 */
export function BindingModeBadge({ bound, className }: { bound: boolean; className?: string }) {
  return (
    <Badge kind="neutral" className={className}>
      {bound ? "binding key" : "master key only"}
    </Badge>
  );
}

/**
 * A present secret whose current version is binding-key protected, with the
 * consequence for clients in the tooltip. Same Tooltip/Badge recipe as the
 * pipeline's drift badge so the two sit on one row without visual seams.
 */
export function BindingKeyBadge({ version, className }: { version: number; className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="badge-tip" />}>
        <BindingModeBadge bound className={className} />
      </TooltipTrigger>
      <TooltipContent>
        Clients must present this secret's binding key to read v{version}. Rotate or unbind it from
        Manage.
      </TooltipContent>
    </Tooltip>
  );
}
