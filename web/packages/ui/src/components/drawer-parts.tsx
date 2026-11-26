import type { ReactNode } from "react";
import s from "./drawer-parts.module.css";

// DrawerSection is a titled part of a detail drawer (the admin's adSec), with room at its right end for a tag or a
// link. Parts are 24px apart; the first sits close under the drawer's head.
export function DrawerSection({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className={s.section}>
      <header className={s.head}>
        <h3>{title}</h3>
        {aside}
      </header>
      {children}
    </section>
  );
}

// FactList is what is known about one thing, in a drawer (the admin's adFacts): the label small on the left, the value
// on the right, a hairline between rows. It draws no box of its own, so it sits in a drawer without nesting one in
// another, which Facts (a card of rows) would.
export function FactList({ rows }: { rows: readonly (readonly [label: string, value: ReactNode])[] }) {
  return (
    <dl className={s.facts}>
      {rows.map(([label, value]) => (
        <div key={label} className={s.row}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
