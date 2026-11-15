import type { ReactNode } from "react";
import s from "./note.module.css";

// Note warns about something the content below depends on (a low balance, a limit nearly reached): a title in words,
// an optional detail and an optional action. The words carry the warning; the dot and tint only draw the eye.
export function Note({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={s.root}>
      <span aria-hidden="true" className={s.dot} />
      <div className={s.text}>
        <strong className={s.title}>{title}</strong>
        {children ? <span className={s.detail}>{children}</span> : null}
      </div>
      {action ? <div className={s.action}>{action}</div> : null}
    </div>
  );
}
