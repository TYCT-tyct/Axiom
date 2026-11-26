import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./drawer-parts.module.css";

type Vars = Record<`--${string}`, string>;

// The parts a drawer's body is made of, from the admin board's detail panels (adPanel in admin_core.js).

// Figure is the one number a drawer is about, large, with a line under it saying what it is (adbalb).
export function Figure({ value, caption }: { value: ReactNode; caption?: ReactNode }) {
  return (
    <div className={s.figure}>
      <b className={s.value}>{value}</b>
      {caption ? <span className={s.caption}>{caption}</span> : null}
    </div>
  );
}

// DrawerSection is a titled part of a drawer, with something small beside the title (adsec): a link, a count, a tag.
// Parts are 24px apart; the first sits close under the drawer's head.
export function DrawerSection({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className={s.section}>
      <header className={s.sectionHead}>
        <h3 className={s.sectionTitle}>{title}</h3>
        {aside}
      </header>
      {children}
    </section>
  );
}

// DrawerFacts is what is known about the thing, label and value on a line with a hairline between lines (adfs), flat on
// the drawer rather than in a card, so it sits in a drawer without nesting one box in another, which Facts would. A value may be as long as the contract allows, so it wraps anywhere. Rows are told
// apart by their label, or by a key when two share one (two subscriptions to the same plan).
export function DrawerFacts({
  rows,
}: {
  rows: readonly (
    | readonly [label: string, value: ReactNode]
    | readonly [label: string, value: ReactNode, key: string]
  )[];
}) {
  return (
    <dl className={s.facts}>
      {rows.map(([label, value, key]) => (
        <div key={key ?? label} className={s.fact}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// QuotaMeter is how much of an allowance is used (adqu): the figure and what it is out of, a bar, and a line saying what
// happens. The bar turns to the warning colour when over says the allowance is used up.
export function QuotaMeter({
  label,
  used,
  of,
  share,
  over = false,
  note,
}: {
  label: string;
  used: ReactNode;
  of: ReactNode;
  // How much of the allowance is used, from 0 to 1.
  share: number;
  over?: boolean;
  note?: ReactNode;
}) {
  const percent = `${(Math.max(0, Math.min(1, share)) * 100).toFixed(1)}%`;
  return (
    <div className={s.quota}>
      <div className={s.quotaHead}>
        <span>{label}</span>
        <b>
          {used}
          <i>
            {" / "}
            {of}
          </i>
        </b>
      </div>
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(Number.parseFloat(percent))}
        className={s.bar}
        style={{ "--p": percent } as Vars}
      >
        <i className={over ? s.over : undefined} />
      </div>
      {note ? <p className={s.quotaNote}>{note}</p> : null}
    </div>
  );
}

export type TallyRow = { label: string; value: ReactNode; sum?: boolean; note?: ReactNode };

// Tally is a short sum worked out in front of the reader (adcalcs): each line a name and an amount, the line that is the
// answer in the text colour and medium weight.
export function Tally({ label, rows }: { label: string; rows: readonly TallyRow[] }) {
  return (
    <dl aria-label={label} className={s.tally}>
      {rows.map((row) => (
        <div key={row.label} className={classes(s.line, row.sum && s.sum)}>
          <dt>
            {row.label}
            {row.note ? <i>{row.note}</i> : null}
          </dt>
          <dd>{row.value}</dd>
        </div>
      ))}
    </dl>
  );
}

// SideBySide puts two records of one thing next to each other (adcmp), each a label over its value.
export function SideBySide({ items }: { items: readonly [TallyRow, TallyRow] }) {
  return (
    <div className={s.pair}>
      {items.map((item) => (
        <div key={item.label}>
          <span>{item.label}</span>
          <b>{item.value}</b>
        </div>
      ))}
    </div>
  );
}

// Tick is a yes in a table of what each column may do (adtick); the words are for a screen reader.
export function Tick({ label }: { label: string }) {
  return (
    <svg className={s.tick} viewBox="0 0 20 20" role="img" aria-label={label}>
      <path d="m5 10.5 3.5 3.5L15 7" />
    </svg>
  );
}
