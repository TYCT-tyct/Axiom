import type { ReactNode } from "react";
import s from "./setting-row.module.css";

// SettingRow is one setting in a card of rows: its name, what it does, and its control. The control names itself (a
// Switch's label, a Field's label), so the row's words are for reading, not a second label. A row that is a step of a
// list is its list item (as="li", inside a plain List), so a screen reader counts the steps.
export function SettingRow({
  title,
  detail,
  as: Element = "div",
  children,
}: {
  title: string;
  detail?: ReactNode;
  as?: "div" | "li";
  children: ReactNode;
}) {
  return (
    <Element className={s.root}>
      <div className={s.label}>
        <span className={s.title}>{title}</span>
        {detail ? <div className={s.detail}>{detail}</div> : null}
      </div>
      <div className={s.control}>{children}</div>
    </Element>
  );
}
