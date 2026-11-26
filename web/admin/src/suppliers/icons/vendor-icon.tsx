import type { ReactNode } from "react";
import s from "./vendor-icon.module.css";

// The marks of the channel types, by the descriptor's vendor (docs/domains/suppliers-direct.md「后台的「添加上游账号」」):
// plain geometric shapes of our own, not the makers' artwork, drawn in currentColor on a 24-unit grid so they follow
// the theme. A vendor without one shows the first letter of the type's name.
const marks: Record<string, ReactNode> = {
  // a hexagon around a point: a model maker of the OpenAI formats
  openai: (
    <>
      <path d="M12 3.5 19.4 7.75v8.5L12 20.5l-7.4-4.25v-8.5Z" />
      <circle cx="12" cy="12" r="2.5" />
    </>
  ),
  // two strokes and a bar
  anthropic: <path d="M5.5 19.5 12 4.5l6.5 15M8.2 13.5h7.6" />,
  // a four-pointed spark
  google: <path d="M12 3c.8 4.6 4.4 8.2 9 9-4.6.8-8.2 4.4-9 9-.8-4.6-4.4-8.2-9-9 4.6-.8 8.2-4.4 9-9Z" />,
  // a crescent
  moonshotai: <path d="M15.5 4.2A8 8 0 1 0 19.8 15 6.5 6.5 0 0 1 15.5 4.2Z" />,
  // a wave over a deeper one
  deepseek: <path d="M3.5 10c2.8-3 5.7-3 8.5 0s5.7 3 8.5 0M3.5 15.5c2.8-3 5.7-3 8.5 0s5.7 3 8.5 0" />,
  // a cloud
  "google-cloud": <path d="M7.5 18.5a4 4 0 0 1-.6-7.95A5.5 5.5 0 0 1 17.6 9.6a4.5 4.5 0 0 1-.6 8.9Z" />,
  // a cube
  aws: <path d="M12 3.5 19.5 7.5v9L12 20.5 4.5 16.5v-9ZM4.5 7.5 12 11.5l7.5-4M12 11.5v9" />,
  // a wedge on a base
  azure: <path d="M10.5 4.5h4L20 19.5H4l6.5-15ZM8 15.5h8" />,
};

export function VendorIcon({ vendor, name }: { vendor?: string; name: string }) {
  const mark = vendor ? marks[vendor] : undefined;
  return (
    <span className={s.tile} aria-hidden="true">
      {mark ? (
        <svg viewBox="0 0 24 24" className={s.mark} aria-hidden="true">
          {mark}
        </svg>
      ) : (
        <span className={s.letter}>{[...name.trim()][0]?.toUpperCase() ?? "?"}</span>
      )}
    </span>
  );
}
