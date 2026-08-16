import { useId } from "react";

/**
 * The KMS mark: a geometric K with a keyway punched through its join.
 * Keeping the tile in CSS lets the same glyph inherit every branded size.
 */
export function LogoMark() {
  const maskId = useId();

  return (
    <span className="logo" aria-hidden="true">
      <svg viewBox="0 0 32 32" fill="none" focusable="false">
        <mask id={maskId} maskUnits="userSpaceOnUse" x="7" y="6" width="20" height="20">
          <rect x="7" y="6" width="20" height="20" fill="white" />
          <path d="m15.1 13.35 2.65 2.65-2.65 2.65L12.45 16l2.65-2.65Z" fill="black" />
        </mask>
        <g mask={`url(#${maskId})`} fill="currentColor">
          <rect x="7.75" y="6.75" width="4.5" height="18.5" rx="1.35" />
          <path d="m11.25 14.65 9-7.9h5.5L15.1 16.2h-3.85v-1.55Z" />
          <path d="m14.75 14.75 11.15 10.5h-5.6l-9.05-8.6v-1.9h3.5Z" />
        </g>
      </svg>
    </span>
  );
}
