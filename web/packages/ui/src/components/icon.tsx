import type { ReactNode, SVGProps } from "react";

// The design's own line icons: a 16 × 16 grid, 1.5 stroke, round caps, drawn in the current text colour. An icon never
// carries meaning alone: text sits beside it, or the control that holds it has an aria-label.
const icons = {
  search: (
    <>
      <circle cx="7" cy="7" r="4.75" />
      <path d="m10.5 10.5 3.25 3.25" />
    </>
  ),
  right: <path d="m6 3.5 4.5 4.5L6 12.5" />,
  down: <path d="m4 6 4 4 4-4" />,
  grid: (
    <>
      <rect x="2.25" y="2.25" width="4.5" height="4.5" rx="1.2" />
      <rect x="9.25" y="2.25" width="4.5" height="4.5" rx="1.2" />
      <rect x="2.25" y="9.25" width="4.5" height="4.5" rx="1.2" />
      <rect x="9.25" y="9.25" width="4.5" height="4.5" rx="1.2" />
    </>
  ),
  list: <path d="M5.5 4h8M5.5 8h8M5.5 12h8M2.5 4h.01M2.5 8h.01M2.5 12h.01" />,
  chart: <path d="M2.5 13.5h11M4.5 11V8M8 11V4.5M11.5 11V6.5" />,
  key: (
    <>
      <circle cx="5.5" cy="10.5" r="3" />
      <path d="m7.6 8.4 5.4-5.4M11 5l1.5 1.5" />
    </>
  ),
  sliders: (
    <>
      <path d="M2.5 4.5h6M11.5 4.5h2M2.5 11.5h2M7.5 11.5h6" />
      <circle cx="10" cy="4.5" r="1.5" />
      <circle cx="6" cy="11.5" r="1.5" />
    </>
  ),
  cube: (
    <>
      <path d="M8 1.75 13.5 4.75v6.5L8 14.25 2.5 11.25v-6.5z" />
      <path d="M2.5 4.75 8 7.75l5.5-3M8 7.75v6.5" />
    </>
  ),
  card: (
    <>
      <rect x="1.75" y="3.25" width="12.5" height="9.5" rx="1.75" />
      <path d="M1.75 6.25h12.5" />
    </>
  ),
  shield: <path d="M8 1.75 13 3.75v4c0 3-2.1 5.2-5 6.5-2.9-1.3-5-3.5-5-6.5v-4z" />,
  lock: (
    <>
      <rect x="3" y="7" width="10" height="7" rx="1.75" />
      <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
    </>
  ),
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
  close: <path d="m4 4 8 8M12 4l-8 8" />,
  alert: (
    <>
      <circle cx="8" cy="8" r="5.75" />
      <path d="M8 5v3.5M8 11h.01" />
    </>
  ),
  check: <path d="m3.25 8.25 3 3 6.5-6.5" />,
} satisfies Record<string, ReactNode>;

export type IconName = keyof typeof icons;

export function Icon({ name, ...props }: { name: IconName } & SVGProps<SVGSVGElement>) {
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
      {icons[name]}
    </svg>
  );
}
