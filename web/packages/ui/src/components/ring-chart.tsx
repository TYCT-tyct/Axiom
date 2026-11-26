import { type Key, type ReactNode, useState } from "react";
import { classes } from "../classes";
import s from "./ring-chart.module.css";

// The colour of a part: the data colour in its three steps, a category colour (--c0…--c4, a model's own), grey, red; the
// status colours of a score (ok, warn, bad), and the track the rest of a score is drawn on.
type Swatch = "data" | "data2" | "data3" | "c0" | "c1" | "c2" | "c3" | "c4" | "muted" | "bad" | "ok" | "warn" | "track";

export type RingPart = {
  key: Key;
  label: string;
  value: number;
  // The share as it reads in the list ("42%"), formatted by the caller.
  share: string;
  colour: Swatch;
  // Set the name in the code face, for a model or an identifier.
  mono?: boolean;
};

// The board's geometry: a ring of radius 42 in a 120 box, cut 2 units between parts, its view trimmed to the stroke.
const radius = 42;
const around = 2 * Math.PI * radius;

// Each part's stretch of the ring as a dash and where it starts, cut short so neighbours stay apart.
function arcsOf(parts: readonly RingPart[]) {
  const total = parts.reduce((sum, part) => sum + Math.max(0, part.value), 0) || 1;
  const cut = parts.length > 1 ? 2 : 0;
  let at = 0;
  return parts.map((part) => {
    const length = (Math.max(0, part.value) / total) * around;
    const drawn = Math.max(0.5, length - cut);
    const arc = { dash: `${drawn.toFixed(2)} ${(around - drawn).toFixed(2)}`, offset: (-at).toFixed(2) };
    at += length;
    return arc;
  });
}

// What lights a part: a pointer over its arc or its row, or focus on its row.
type Lighting = { onPointerEnter: () => void; onPointerLeave: () => void };

function Row({
  part,
  lit,
  lighting,
  onSelect,
}: {
  part: RingPart;
  lit: boolean;
  lighting: Lighting & { onFocus: () => void; onBlur: () => void };
  onSelect?: (key: Key) => void;
}) {
  const content = (
    <>
      <i className={classes(s.swatch, s[part.colour])} />
      <span className={classes(s.name, part.mono && s.mono)}>{part.label}</span>
      <span className={s.share}>{part.share}</span>
    </>
  );
  const className = classes(s.row, lit && s.lit);
  return (
    <li {...lighting}>
      {onSelect ? (
        <button type="button" className={className} onClick={() => onSelect(part.key)}>
          {content}
        </button>
      ) : (
        <div className={className}>{content}</div>
      )}
    </li>
  );
}

// RingChart is a whole split into parts (the admin's adRing, with the model mix's list beside it): a thick ring, a
// figure in its middle, and a row per part. The ring and the list point at each other: a pointer on a row, or focus on
// it, thickens its part of the ring, and a pointer on a part lights its row. With onSelect a row is a button that opens
// the part. The ring is a picture whose label says every part and its share. With an aside it stands beside that instead
// of the list of parts, for a figure with words of its own (the health score and what was taken off it); its label is then
// the one given.
export function RingChart({
  label,
  parts,
  centre,
  centreLabel,
  size = 132,
  onSelect,
  aside,
}: {
  label: string;
  parts: readonly RingPart[];
  centre?: ReactNode;
  centreLabel?: string;
  size?: number;
  onSelect?: (key: Key) => void;
  aside?: ReactNode;
}) {
  const [hot, setHot] = useState<number | null>(null);
  const arcs = arcsOf(parts);
  const summary =
    aside === undefined ? `${label}: ${parts.map((part) => `${part.label} ${part.share}`).join(", ")}` : label;
  const lighting = (i: number): Lighting => ({ onPointerEnter: () => setHot(i), onPointerLeave: () => setHot(null) });
  return (
    <div className={classes(s.root, aside !== undefined && s.beside)}>
      <div
        role="img"
        aria-label={summary}
        className={s.ring}
        style={{ "--size": `${size}px` } as Record<`--${string}`, string>}
      >
        <svg viewBox="9 9 102 102" aria-hidden="true">
          <g transform="rotate(-90 60 60)">
            {parts.map((part, i) => (
              <circle
                key={part.key}
                className={classes(s.arc, s[part.colour], hot === i && s.hot)}
                cx="60"
                cy="60"
                r={radius}
                strokeDasharray={arcs[i]?.dash}
                strokeDashoffset={arcs[i]?.offset}
                {...lighting(i)}
              />
            ))}
          </g>
        </svg>
        {centre !== undefined ? (
          <div className={s.centre} aria-hidden="true">
            <b>{centre}</b>
            {centreLabel ? <span>{centreLabel}</span> : null}
          </div>
        ) : null}
      </div>
      {aside !== undefined ? (
        <div className={s.aside}>{aside}</div>
      ) : (
        <ul className={s.list}>
          {parts.map((part, i) => (
            <Row
              key={part.key}
              part={part}
              lit={hot === i}
              lighting={{ ...lighting(i), onFocus: () => setHot(i), onBlur: () => setHot(null) }}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
