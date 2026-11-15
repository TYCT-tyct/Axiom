import type { ReactNode } from "react";
import s from "./page-head.module.css";
import { usePageTitle } from "./page-title";

// PageHead is the head of a page: its level-one heading, which also names the tab, an optional lede and the page's
// actions.
export function PageHead({ title, lede, actions }: { title: string; lede?: string; actions?: ReactNode }) {
  usePageTitle(title);
  return (
    <div className={s.root}>
      <div>
        <h1 className={s.title}>{title}</h1>
        {lede ? <p className={s.lede}>{lede}</p> : null}
      </div>
      {actions ? <div className={s.actions}>{actions}</div> : null}
    </div>
  );
}
