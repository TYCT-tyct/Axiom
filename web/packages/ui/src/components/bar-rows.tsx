import { Slot } from "radix-ui";
import { cloneElement, type Key, type ReactElement, type ReactNode } from "react";
import { classes } from "../classes";
import s from "./bar-rows.module.css";

type BarRow = {
  key: Key;
  label: string;
  // What the bar measures, on the scale shared by all rows (the largest fills the track).
  value: number;
  // The figure at the row's end, as it reads.
  end: string;
  // The data colour, red for what went wrong, grey for what is only counted.
  tone?: "data" | "bad" | "muted";
  // A link the row becomes, given empty (a router link): its content goes inside it.
  link?: ReactElement<{ children?: ReactNode }>;
};

// BarRows is the admin's horizontal bars (adhb): a name, a bar and a figure per row, as the completion-time spread and
// the failure causes are drawn. The bars are drawn against the largest value, never less than a sliver so a small count
// stays visible; the figure at the end says the value in words.
export function BarRows({ label, rows }: { label: string; rows: readonly BarRow[] }) {
  const top = Math.max(1e-9, ...rows.map((row) => Math.max(0, row.value)));
  return (
    <ul aria-label={label} className={s.root}>
      {rows.map((row) => {
        const content = (
          <>
            <span className={s.name} title={row.label}>
              {row.label}
            </span>
            <span className={s.track} aria-hidden="true">
              <i
                className={classes(s.fill, s[row.tone ?? "data"])}
                style={
                  { "--w": `${Math.max(1.5, (Math.max(0, row.value) / top) * 100).toFixed(1)}%` } as Record<
                    string,
                    string
                  >
                }
              />
            </span>
            <span className={s.end}>{row.end}</span>
          </>
        );
        return (
          <li key={row.key}>
            {row.link ? (
              <Slot.Root className={classes(s.row, s.link)}>{cloneElement(row.link, undefined, content)}</Slot.Root>
            ) : (
              <div className={s.row}>{content}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}
