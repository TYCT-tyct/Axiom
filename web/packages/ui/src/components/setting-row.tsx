import type { ReactNode } from "react";
import { board } from "../board-classes";
import s from "./setting-row.module.css";

// SettingRow is one setting in a card of rows (.qrow/.qrl/.qrc from AxApp/regions.css): its name, what it does, and its
// control. The control names itself (a Switch's label, a Field's label), so the row's words are for reading, not a
// second label. A row that is a step of a list is its list item (as="li", inside a plain List).
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
    <Element className={board.qrow}>
      <div className={board.qrl}>
        <b>{title}</b>
        {detail ? <span className={s.detail}>{detail}</span> : null}
      </div>
      <div className={board.qrc}>{children}</div>
    </Element>
  );
}
