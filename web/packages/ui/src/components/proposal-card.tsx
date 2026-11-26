import { type ReactNode, useId } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./proposal-card.module.css";

// ProposalCard is one change waiting for a decision (the admin's adprop): what kind it is and what it does, who asked and
// when, why and on what grounds on the left, what it changes and how far its limits allow on the right, and the buttons
// under a hairline, so it can be decided without opening anything. leaving folds it away once it is decided: it fades,
// shrinks to nothing and takes its own gap with it; the page drops it when the fold is done.
export function ProposalCard({
  kind,
  title,
  expiry,
  meta,
  why,
  change,
  note,
  actions,
  leaving = false,
}: {
  // The class of the change ("给账户赠额"), a small grey chip before the title.
  kind: string;
  title: string;
  // How long it has left, at the header's right.
  expiry?: string;
  // Who asked, when, about what: small facts in a row under the title.
  meta?: ReactNode;
  // The reason and the grounds, left.
  why: ReactNode;
  // The change and its limits, right.
  change?: ReactNode;
  // A line before the buttons, for what stops a button.
  note?: string;
  actions: ReactNode;
  leaving?: boolean;
}) {
  const headingId = useId();
  return (
    <div className={classes(s.fold, leaving && s.leaving)} inert={leaving}>
      <section aria-labelledby={headingId} className={classes(board.qcard, s.card)}>
        <header className={s.head}>
          <div>
            <span className={s.kind}>{kind}</span>
            <h2 id={headingId}>{title}</h2>
          </div>
          {expiry ? <span className={s.expiry}>{expiry}</span> : null}
        </header>
        {meta ? <div className={s.meta}>{meta}</div> : null}
        <div className={s.body}>
          <div>{why}</div>
          {change ? <div>{change}</div> : null}
        </div>
        <footer className={s.foot}>
          {note ? <span className={s.note}>{note}</span> : null}
          {actions}
        </footer>
      </section>
    </div>
  );
}
