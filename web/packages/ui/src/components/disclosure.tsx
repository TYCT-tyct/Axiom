import { type ReactNode, useState } from "react";
import s from "./disclosure.module.css";
import { Icon } from "./icon";

// Disclosure keeps what most readers do not need folded under a line that says what it holds (the key dialog's
// "more restrictions"); it is the browser's details element, so it opens by keyboard and find-in-page reaches inside.
// As one of a channel's piles of models (the admin's .adpile), it says how many it holds (count) and what happens to
// them (note), and a lazy one draws what it holds only once first opened, so what is never opened is never read.
export function Disclosure({
  summary,
  open,
  count,
  note,
  lazy = false,
  children,
}: {
  summary: string;
  open?: boolean;
  count?: string;
  note?: string;
  lazy?: boolean;
  children: ReactNode;
}) {
  const [opened, setOpened] = useState(open ?? false);
  return (
    <details
      className={count === undefined ? s.root : s.pile}
      open={open}
      onToggle={(event) => event.currentTarget.open && setOpened(true)}
    >
      <summary className={s.summary}>
        <Icon name="right" className={s.chevron} />
        {count === undefined ? (
          summary
        ) : (
          <>
            <b>{summary}</b>
            <em className={s.count}>{count}</em>
            {note ? <span className={s.note}>{note}</span> : null}
          </>
        )}
      </summary>
      <div className={s.body}>{!lazy || opened ? children : null}</div>
    </details>
  );
}
