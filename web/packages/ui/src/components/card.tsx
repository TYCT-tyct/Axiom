import { type ReactNode, useId } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./card.module.css";
import { Tooltip } from "./tooltip";

// Card is a titled block of a page (.qcard/.flush/.rows/.danger and header .qch from AxApp/regions.css). A card with a
// title is a section named by it, so a screen reader can list and jump to it. scope says in the card's top right how far
// a change made in it reaches (.adscope in admin_set.js), the tip saying it in a sentence. foot is the line under a
// card's table (the admin's .adtfoot): a note on what the rows are and, at its end, what to do about them.
export function Card({
  id,
  title,
  lede,
  actions,
  scope,
  layout = "padded",
  danger = false,
  foot,
  children,
}: {
  id?: string;
  title?: string;
  lede?: string;
  actions?: ReactNode;
  scope?: { label: string; tip: string };
  layout?: "padded" | "flush" | "rows";
  danger?: boolean;
  foot?: ReactNode;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section
      id={id}
      aria-labelledby={title ? headingId : undefined}
      className={classes(board.qcard, layout !== "padded" && board[layout], danger && board.danger, s.root)}
    >
      {title || actions || scope ? (
        <CardHead headingId={headingId} title={title} lede={lede} actions={actions} scope={scope} />
      ) : null}
      {children}
      {foot ? <footer className={s.foot}>{foot}</footer> : null}
    </section>
  );
}

// The head of a card: its title and lede on the left, how far a change reaches and its actions on the right.
function CardHead({
  headingId,
  title,
  lede,
  actions,
  scope,
}: {
  headingId: string;
  title?: string;
  lede?: string;
  actions?: ReactNode;
  scope?: { label: string; tip: string };
}) {
  return (
    <div className={board.qch}>
      <div>
        {title ? <h2 id={headingId}>{title}</h2> : null}
        {lede ? <p>{lede}</p> : null}
      </div>
      {actions || scope ? (
        <div className={s.actions}>
          {scope ? (
            <span className={s.scope}>
              <Tooltip text={scope.tip}>{scope.label}</Tooltip>
            </span>
          ) : null}
          {actions}
        </div>
      ) : null}
    </div>
  );
}
