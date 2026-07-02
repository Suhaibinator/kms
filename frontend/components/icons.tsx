// Minimal stroke icons (currentColor). Kept tiny and dependency-free.

type IconProps = { size?: number };

function svg(path: React.ReactNode, size: number) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.9}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {path}
    </svg>
  );
}

export const Icon = {
  dashboard: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <rect x="3" y="3" width="7" height="9" rx="1" />
        <rect x="14" y="3" width="7" height="5" rx="1" />
        <rect x="14" y="12" width="7" height="9" rx="1" />
        <rect x="3" y="16" width="7" height="5" rx="1" />
      </>,
      size,
    ),
  namespace: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M3 7l9-4 9 4-9 4-9-4z" />
        <path d="M3 12l9 4 9-4" />
        <path d="M3 17l9 4 9-4" />
      </>,
      size,
    ),
  parameter: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M4 7h16" />
        <path d="M4 12h10" />
        <path d="M4 17h13" />
      </>,
      size,
    ),
  secret: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <rect x="4" y="10" width="16" height="10" rx="2" />
        <path d="M8 10V7a4 4 0 0 1 8 0v3" />
        <circle cx="12" cy="15" r="1.3" />
      </>,
      size,
    ),
  policy: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M12 3l7 3v5c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6l7-3z" />
        <path d="M9.5 12l1.8 1.8L15 10" />
      </>,
      size,
    ),
  identity: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <circle cx="12" cy="8" r="3.4" />
        <path d="M5 20a7 7 0 0 1 14 0" />
      </>,
      size,
    ),
  audit: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M8 3h8l4 4v14H4V3h4z" />
        <path d="M8 12h8" />
        <path d="M8 16h6" />
        <path d="M9 8h2" />
      </>,
      size,
    ),
  subscribers: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M4 11a9 9 0 0 1 9 9" />
        <path d="M4 4a16 16 0 0 1 16 16" />
        <circle cx="5" cy="19" r="1.6" />
      </>,
      size,
    ),
  health: ({ size = 16 }: IconProps) =>
    svg(<path d="M3 12h4l2 6 4-14 2 8h6" />, size),
  logout: ({ size = 16 }: IconProps) =>
    svg(
      <>
        <path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3" />
        <path d="M10 17l-5-5 5-5" />
        <path d="M15 12H5" />
      </>,
      size,
    ),
  menu: ({ size = 18 }: IconProps) =>
    svg(
      <>
        <path d="M4 7h16" />
        <path d="M4 12h16" />
        <path d="M4 17h16" />
      </>,
      size,
    ),
  close: ({ size = 18 }: IconProps) =>
    svg(
      <>
        <path d="M6 6l12 12" />
        <path d="M18 6L6 18" />
      </>,
      size,
    ),
};
