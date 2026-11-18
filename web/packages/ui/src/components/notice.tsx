import type { ReactNode } from "react";
import { Icon } from "./icon";
import s from "./notice.module.css";

// Notice is a one-line message across the top of the page that says what changed and what to do about it.
export function Notice({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div role="status" className={s.root}>
      <Icon name="refresh" className={s.icon} />
      <span className={s.text}>{children}</span>
      {action}
    </div>
  );
}
