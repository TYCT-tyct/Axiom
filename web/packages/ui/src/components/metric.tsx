import { type Money, useFormat } from "../format";
import s from "./metric.module.css";

// Metric is one headline number of a page (the board's .qkpi): a label, the value, and optionally how it moved. A count
// is a number, money is an exact amount; both are shown by format.ts for the page's language, so nothing here rounds or
// converts. delta is the change as a ratio (0.125 is +12.5%) and deltaLabel says against what ("vs yesterday"); the
// sign is in the words, never only in a colour.
export function Metric({
  label,
  value,
  delta,
  deltaLabel,
  timeZone,
  bare = false,
}: {
  label: string;
  value: number | Money;
  delta?: number;
  deltaLabel?: string;
  timeZone?: string;
  // bare draws no block of its own, for a figure that sits inside a card already.
  bare?: boolean;
}) {
  const format = useFormat(timeZone);
  return (
    <div className={bare ? s.bare : s.root}>
      <span className={s.label}>{label}</span>
      <b className={s.value}>{typeof value === "number" ? format.number(value) : format.money(value)}</b>
      {delta === undefined ? null : (
        <em className={s.delta}>
          {format.percent(delta)}
          {deltaLabel ? ` ${deltaLabel}` : ""}
        </em>
      )}
    </div>
  );
}
