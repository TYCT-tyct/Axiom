import type { ReactNode } from "react";
import { Card } from "./card";
import s from "./facts.module.css";

// Facts is what is known about one thing, label beside value, in a card of rows (the board's .qrow): a value may be as
// long as the contract allows, so it wraps anywhere. On a phone the label sits over its value.
export function Facts({
  title,
  actions,
  rows,
}: {
  title?: string;
  actions?: ReactNode;
  rows: readonly (readonly [label: string, value: ReactNode])[];
}) {
  return (
    <Card title={title} actions={actions} layout="rows">
      <dl className={s.list}>
        {rows.map(([label, value]) => (
          <div key={label} className={s.row}>
            <dt className={s.label}>{label}</dt>
            <dd className={s.value}>{value}</dd>
          </div>
        ))}
      </dl>
    </Card>
  );
}
