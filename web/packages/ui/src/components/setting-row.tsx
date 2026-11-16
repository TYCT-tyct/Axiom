import type { ReactNode } from "react";
import s from "./setting-row.module.css";

// SettingRow is one setting in a card of rows: its name, what it does, and its control. The control names itself (a
// Switch's label, a Field's label), so the row's words are for reading, not a second label.
export function SettingRow({ title, detail, children }: { title: string; detail?: ReactNode; children: ReactNode }) {
  return (
    <div className={s.root}>
      <div className={s.label}>
        <span className={s.title}>{title}</span>
        {detail ? <span className={s.detail}>{detail}</span> : null}
      </div>
      <div className={s.control}>{children}</div>
    </div>
  );
}
