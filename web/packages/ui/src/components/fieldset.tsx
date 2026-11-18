import type { ReactNode } from "react";
import s from "./fieldset.module.css";

// Fieldset groups the fields of one part of a form under its legend, so a screen reader names the group with each
// field; the group sits in an outlined box, as a card's section would, without being a card of its own.
export function Fieldset({ legend, hint, children }: { legend: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <fieldset className={s.root}>
      <legend className={s.legend}>{legend}</legend>
      {hint ? <p className={s.hint}>{hint}</p> : null}
      <div className={s.body}>{children}</div>
    </fieldset>
  );
}
