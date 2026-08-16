import {
  Activity,
  Boxes,
  FileText,
  Layers,
  LayoutDashboard,
  List,
  LockKeyhole,
  LogOut,
  Menu,
  Radio,
  ShieldCheck,
  Tags,
  UserRound,
  X,
} from "lucide-react";

// Keep the application-level icon names stable while using Lucide for the
// actual SVGs. This lets page components select semantic icons without
// duplicating paths or importing individual icon sets.
type IconProps = { size?: number };

function lucide(Component: typeof LayoutDashboard, size: number) {
  return <Component size={size} strokeWidth={1.9} aria-hidden />;
}

export const Icon = {
  dashboard: ({ size = 16 }: IconProps) => lucide(LayoutDashboard, size),
  namespace: ({ size = 16 }: IconProps) => lucide(Layers, size),
  application: ({ size = 16 }: IconProps) => lucide(Boxes, size),
  parameter: ({ size = 16 }: IconProps) => lucide(List, size),
  secret: ({ size = 16 }: IconProps) => lucide(LockKeyhole, size),
  policy: ({ size = 16 }: IconProps) => lucide(ShieldCheck, size),
  identity: ({ size = 16 }: IconProps) => lucide(UserRound, size),
  audit: ({ size = 16 }: IconProps) => lucide(FileText, size),
  subscribers: ({ size = 16 }: IconProps) => lucide(Radio, size),
  health: ({ size = 16 }: IconProps) => lucide(Activity, size),
  release: ({ size = 16 }: IconProps) => lucide(Tags, size),
  logout: ({ size = 16 }: IconProps) => lucide(LogOut, size),
  menu: ({ size = 18 }: IconProps) => lucide(Menu, size),
  close: ({ size = 18 }: IconProps) => lucide(X, size),
};
