import type { ReactNode } from "react";
import { Card } from "./card";
import s from "./stat.module.css";

// Stat is one figure the user looks at first: what it is, then the number, large (the board's .qbig). The number may be
// as long as an amount can be, so it wraps rather than pushing the page wider, filling each line: breaking only where
// a word may would leave a minus sign alone on the first line.
export function Stat({ label, value, note }: { label: string; value: ReactNode; note?: ReactNode }) {
  return (
    <Card>
      <p className={s.label}>{label}</p>
      <p className={s.value}>{value}</p>
      {note ? <p className={s.note}>{note}</p> : null}
    </Card>
  );
}
