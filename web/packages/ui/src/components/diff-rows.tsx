import type { ReactNode } from "react";
import s from "./diff-rows.module.css";

export type DiffRow = { label: string; before?: ReactNode; after: ReactNode };

// DiffRows shows what a change does before it is made (adDiffRow in admin_core.js): one row per changed field, its name
// on the left, the old value struck through and the new one after an arrow on the right. A field that had no value (a
// new secret, an empty setting) shows only the new one. The values are <del> and <ins>, so a screen reader hears which
// is which without the strike.
export function DiffRows({ rows }: { rows: readonly DiffRow[] }) {
  return (
    <dl className={s.root}>
      {rows.map((row) => (
        <div key={row.label} className={s.row}>
          <dt>{row.label}</dt>
          <dd className={s.values}>
            {row.before != null && row.before !== "" ? <del className={s.before}>{row.before}</del> : null}
            <svg className={s.arrow} viewBox="0 0 20 20" aria-hidden="true">
              <path d="M4 10h11M11.5 6.5 15 10l-3.5 3.5" />
            </svg>
            <ins className={s.after}>{row.after}</ins>
          </dd>
        </div>
      ))}
    </dl>
  );
}
