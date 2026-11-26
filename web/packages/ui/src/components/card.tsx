import { type ReactNode, useId } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./card.module.css";
import { Tooltip } from "./tooltip";

// Card is a titled block of a page (.qcard/.flush/.rows/.danger and header .qch from AxApp/regions.css). A card with a
// title is a section named by it, so a screen reader can list and jump to it. scope says in the card's top right how far
// a change made in it reaches (.adscope in admin_set.js), the tip saying it in a sentence.
export function Card({
  id,
  title,
  lede,
  actions,
  scope,
  layout = "padded",
  danger = false,
  children,
}: {
  id?: string;
  title?: string;
  lede?: string;
  actions?: ReactNode;
  scope?: { label: string; tip: string };
  layout?: "padded" | "flush" | "rows";
  danger?: boolean;
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
      ) : null}
      {children}
    </section>
  );
}
