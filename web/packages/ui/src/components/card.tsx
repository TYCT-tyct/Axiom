import { type ReactNode, useId } from "react";
import { classes } from "../classes";
import s from "./card.module.css";

// Card is a titled block of a page. A card with a title is a section named by it, so a screen reader can list and jump
// to it.
export function Card({
  id,
  title,
  lede,
  actions,
  layout = "padded",
  danger = false,
  children,
}: {
  id?: string;
  title?: string;
  lede?: string;
  actions?: ReactNode;
  layout?: "padded" | "flush" | "rows";
  danger?: boolean;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section
      id={id}
      aria-labelledby={title ? headingId : undefined}
      className={classes(s.root, layout !== "padded" && s[layout], danger && s.danger)}
    >
      {title || actions ? (
        <div className={s.head}>
          <div>
            {title ? (
              <h2 id={headingId} className={s.title}>
                {title}
              </h2>
            ) : null}
            {lede ? <p className={s.lede}>{lede}</p> : null}
          </div>
          {actions ? <div className={s.actions}>{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}
