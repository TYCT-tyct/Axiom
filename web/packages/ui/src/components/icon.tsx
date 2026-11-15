import type { ReactNode, SVGProps } from "react";
import { classes } from "../classes";
import s from "./icon.module.css";

// The design's icons (AxIcons.dc.html): a 20 × 20 grid, 1.5 stroke, round caps, in the current text colour. Each is
// drawn twice, an outline group and a filled group; selected shows the filled one, with its signal part (`sig`) in the
// accent. `data-p` names the parts the stylesheet moves on hover (a0…a4) and how the filled group treats them: `cut` is
// drawn in the background colour (--cut) to cut a line out of the fill, `nf` stays unfilled, `ff`/`oo` exist only in
// the filled/outline group, `fo` is always filled, `gn` is a gauge needle. An icon never carries meaning alone: text
// sits beside it, or the control that holds it has an aria-label.
//
// more, warn and updown have hover motions on the board but no drawing there; they are drawn here on the same grid.
const icons = {
  overview: (
    <>
      <rect data-p="a1" x="3" y="3" width="6" height="8" rx="2" />
      <rect data-p="a2" x="11" y="3" width="6" height="5" rx="2" />
      <rect data-p="a3 sig" x="11" y="10" width="6" height="7" rx="2" />
      <rect data-p="a4" x="3" y="13" width="6" height="4" rx="2" />
    </>
  ),
  usage: (
    <>
      <rect data-p="a1" x="3.25" y="10" width="3" height="7" rx="1" />
      <rect data-p="a2" x="8.5" y="6" width="3" height="11" rx="1" />
      <rect data-p="a3 sig" x="13.75" y="3" width="3" height="14" rx="1" />
    </>
  ),
  logs: (
    <>
      <rect data-p="a1" x="3.5" y="3" width="13" height="14" rx="2.75" />
      <path data-p="a2 cut" d="M7 7.25h6M7 10.25h6M7 13.25h3.5" />
    </>
  ),
  keys: (
    <g data-p="a1">
      <circle cx="6.75" cy="10" r="3.5" />
      <path d="M10.25 10H17M14.5 10v2.5M17 10v2" />
    </g>
  ),
  presets: (
    <>
      <path data-p="a1 sig" d="M10 3 17 6.75 10 10.5 3 6.75z" />
      <path data-p="a2 nf" d="m3 10.25 7 3.75 7-3.75" />
      <path data-p="a3 nf" d="m3 13.5 7 3.75 7-3.75" />
    </>
  ),
  route: (
    <>
      <circle data-p="a0 sig" cx="3.5" cy="10" r="1.5" />
      <path data-p="a1 nf" d="M5 10h3c3 0 3-5 6-5h2.5M8 10c3 0 3 5 6 5h2.5" />
    </>
  ),
  byok: (
    <>
      <path data-p="a1 sig" d="M7.25 2.75v3.5M12.75 2.75v3.5" />
      <path data-p="a2" d="M5 6.5h10v3a5 5 0 0 1-10 0z" />
      <path data-p="a3" d="M10 14.5v2.75" />
    </>
  ),
  budget: (
    <>
      <path data-p="a1 oo" d="M5.05 15.95A7 7 0 1 1 14.95 15.95" />
      <path data-p="a1 ff" d="M5.05 15.95A7 7 0 1 1 14.95 15.95z" />
      <path data-p="a2 cut gn" d="M10 11V5.5" />
      <circle data-p="a3 cut" cx="10" cy="11" r=".6" />
    </>
  ),
  privacy: (
    <>
      <path data-p="a1" d="M10 2.75 16 5v5c0 3.6-2.6 6.2-6 7.25C6.6 16.2 4 13.6 4 10V5z" />
      <path data-p="a2 cut" d="m7.5 10 1.75 1.75L12.75 8" />
    </>
  ),
  security: (
    <>
      <rect data-p="a1" x="4" y="9" width="12" height="8.5" rx="2.25" />
      <path data-p="a2 nf" d="M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9" />
      <path data-p="a3 cut" d="M10 12.25v2" />
    </>
  ),
  wallet: (
    <>
      <path data-p="a1 nf" d="M3 6.5A2.5 2.5 0 0 1 5.5 4H14.5" />
      <rect data-p="a2" x="3" y="6.5" width="14" height="10" rx="2.5" />
      <circle data-p="a3 sig" cx="13.5" cy="11.5" r="1.1" />
    </>
  ),
  team: (
    <>
      <g data-p="a1">
        <circle cx="7.5" cy="7" r="2.75" />
        <path d="M2.75 16.5c0-2.9 2.1-4.6 4.75-4.6s4.75 1.7 4.75 4.6" />
      </g>
      <g data-p="a2">
        <circle cx="13.75" cy="7.5" r="2.25" />
        <path d="M13.6 11.9c2.3 0 3.9 1.5 3.9 4.1" />
      </g>
    </>
  ),
  settings: (
    <>
      <path data-p="a0" d="M3 6.25h14M3 13.75h14" />
      <circle data-p="a1 sig" cx="7" cy="6.25" r="2" />
      <circle data-p="a2" cx="13" cy="13.75" r="2" />
    </>
  ),
  docs: (
    <path
      data-p="a1"
      d="M4 15.5V4.75A2 2 0 0 1 6 2.75h10v11.5H5.25A1.25 1.25 0 0 0 4 15.5zm0 0a1.75 1.75 0 0 0 1.75 1.75H16"
    />
  ),
  status: <path data-p="a1 nf" d="M2.5 10.25h3l2-4.75 3.5 9 2-4.25h4.5" />,
  collapse: (
    <>
      <rect data-p="a1" x="3" y="3.5" width="14" height="13" rx="2.5" />
      <path data-p="a2 cut" d="M8 3.5v13" />
    </>
  ),
  appearance: (
    <>
      <circle data-p="a1 nf" cx="10" cy="10" r="6.75" />
      <path data-p="a2 fo" d="M10 3.25a6.75 6.75 0 0 1 0 13.5z" />
    </>
  ),
  search: (
    <>
      <circle data-p="a1" cx="8.75" cy="8.75" r="5" />
      <path data-p="a1" d="m12.5 12.5 4 4" />
    </>
  ),
  models: (
    <>
      <path data-p="a1" d="M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z" />
      <path data-p="a2" d="M3.75 6.5 10 10l6.25-3.5M10 10v7" />
    </>
  ),
  copy: (
    <>
      <rect data-p="a1" x="7" y="7" width="9.5" height="9.5" rx="2.25" />
      <path data-p="a2" d="M13 4.5A1.5 1.5 0 0 0 11.5 3.5H5.25A1.75 1.75 0 0 0 3.5 5.25v6.25A1.5 1.5 0 0 0 4.5 13" />
    </>
  ),
  plus: <path data-p="a1" d="M10 4v12M4 10h12" />,
  export: (
    <>
      <path data-p="a1" d="M10 3.5v8.5M6.5 8.75 10 12.25l3.5-3.5" />
      <path data-p="a2" d="M3.5 13.5v1.25A1.75 1.75 0 0 0 5.25 16.5h9.5a1.75 1.75 0 0 0 1.75-1.75V13.5" />
    </>
  ),
  more: (
    <g data-p="a1 fo">
      <circle cx="4.5" cy="10" r="1.25" />
      <circle cx="10" cy="10" r="1.25" />
      <circle cx="15.5" cy="10" r="1.25" />
    </g>
  ),
  close: <path data-p="a1" d="m5 5 10 10M15 5 5 15" />,
  check: <path data-p="a1" d="m4 10.5 4 4 8-9" />,
  warn: (
    <>
      <path data-p="a1" d="M8.7 3.5a1.5 1.5 0 0 1 2.6 0l6 10.5a1.5 1.5 0 0 1-1.3 2.25H4a1.5 1.5 0 0 1-1.3-2.25z" />
      <path data-p="a2 cut" d="M10 7.75v3.5M10 13.6v.01" />
    </>
  ),
  updown: <path data-p="a1 nf" d="m6.5 7.5 3.5-3.5 3.5 3.5M6.5 12.5l3.5 3.5 3.5-3.5" />,
} satisfies Record<string, ReactNode>;

// The current pages' names: those the new set covers draw the new icon, the rest keep their drawing on the old 16 × 16
// grid until their pages move to the new components (#747) and the cleanup removes them.
const aliases = {
  grid: "overview",
  list: "logs",
  chart: "usage",
  key: "keys",
  sliders: "settings",
  cube: "models",
  card: "wallet",
  shield: "privacy",
  lock: "security",
  alert: "warn",
} as const satisfies Record<string, keyof typeof icons>;

const legacy = {
  right: <path d="m6 3.5 4.5 4.5L6 12.5" />,
  down: <path d="m4 6 4 4 4-4" />,
  left: <path d="M10 3.5 5.5 8l4.5 4.5" />,
  refresh: <path d="M2.75 8a5.25 5.25 0 0 1 9-3.7M13.25 8a5.25 5.25 0 0 1-9 3.7M11.75 1.75v2.75H9M4.25 14.25V11.5H7" />,
  moon: <path d="M13.25 9.75A5.75 5.75 0 0 1 6.25 2.75a5.75 5.75 0 1 0 7 7z" />,
  sun: (
    <>
      <circle cx="8" cy="8" r="2.75" />
      <path d="M8 1.75v1.5M8 12.75v1.5M14.25 8h-1.5M3.25 8h-1.5M12.42 3.58l-1.06 1.06M4.64 11.36l-1.06 1.06M12.42 12.42l-1.06-1.06M4.64 4.64 3.58 3.58" />
    </>
  ),
  monitor: (
    <>
      <rect x="1.75" y="2.75" width="12.5" height="8.5" rx="1.5" />
      <path d="M5.5 13.75h5M8 11.25v2.5" />
    </>
  ),
  menu: <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" />,
  pencil: <path d="m10.5 2.75 2.75 2.75-8 8-3.25.75.75-3.25z" />,
  trash: (
    <>
      <path d="M2.75 4.5h10.5M6.25 4.5V3a1 1 0 0 1 1-1h1.5a1 1 0 0 1 1 1v1.5" />
      <path d="m4.25 4.5.5 8.25a1 1 0 0 0 1 .9h4.5a1 1 0 0 0 1-.9l.5-8.25M6.75 7v3.5M9.25 7v3.5" />
    </>
  ),
  logout: <path d="M6.5 2.75H3.75a1 1 0 0 0-1 1v8.5a1 1 0 0 0 1 1H6.5M10 5.25 12.75 8 10 10.75M12.75 8H6" />,
} satisfies Record<string, ReactNode>;

type DesignIconName = keyof typeof icons;
export type IconName = DesignIconName | keyof typeof aliases | keyof typeof legacy;

export const iconNames = Object.keys(icons) as DesignIconName[];

// Icon draws one icon at 16 (beside text in a button, menu or note) or 20 pixels (navigation); selected fills it. The
// hover motion plays when the icon, or a control that sets --icon-hover on hover, is under the pointer, and never under
// reduced motion. The SVG props are for the current pages only: new code sizes and colours an icon by its container.
export function Icon({
  name,
  size = 16,
  selected = false,
  className,
  ...props
}: { name: IconName; size?: 16 | 20; selected?: boolean } & SVGProps<SVGSVGElement>) {
  const design: DesignIconName | undefined =
    name in icons ? (name as DesignIconName) : aliases[name as keyof typeof aliases];
  if (!design) {
    return <LegacyIcon drawing={legacy[name as keyof typeof legacy]} className={className} {...props} />;
  }
  return (
    <svg
      viewBox="0 0 20 20"
      width={size}
      height={size}
      className={classes(s.icon, s[design], selected && s.selected, className)}
      aria-hidden="true"
      {...props}
    >
      <g className={s.o}>{icons[design]}</g>
      <g className={s.f}>{icons[design]}</g>
    </svg>
  );
}

function LegacyIcon({ drawing, ...props }: { drawing: ReactNode } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...props}
    >
      {drawing}
    </svg>
  );
}
