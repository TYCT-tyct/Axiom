import type { ReactNode } from "react";
import s from "./disclosure.module.css";
import { Icon } from "./icon";

// Disclosure keeps what most readers do not need folded under a line that says what it holds (the key dialog's
// "more restrictions"); it is the browser's details element, so it opens by keyboard and find-in-page reaches inside.
export function Disclosure({ summary, open, children }: { summary: string; open?: boolean; children: ReactNode }) {
  return (
    <details className={s.root} open={open}>
      <summary className={s.summary}>
        <Icon name="right" className={s.chevron} />
        {summary}
      </summary>
      <div className={s.body}>{children}</div>
    </details>
  );
}
