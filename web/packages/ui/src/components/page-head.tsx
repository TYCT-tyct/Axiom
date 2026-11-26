import type { ReactNode } from "react";
import s from "./page-head.module.css";
import { usePageTitle } from "./page-title";

// PageHead is the head of a page: its level-one heading, which also names the tab, an optional lede and the page's
// actions. A page of one thing may put where it stands beside the title (tag: the admin's channel status).
export function PageHead({
  title,
  lede,
  actions,
  tag,
}: {
  title: string;
  lede?: string;
  actions?: ReactNode;
  tag?: ReactNode;
}) {
  usePageTitle(title);
  const heading = <h1 className={s.title}>{title}</h1>;
  return (
    <div className={s.root}>
      <div>
        {tag ? (
          <div className={s.named}>
            {heading}
            {tag}
          </div>
        ) : (
          heading
        )}
        {lede ? <p className={s.lede}>{lede}</p> : null}
      </div>
      {actions ? <div className={s.actions}>{actions}</div> : null}
    </div>
  );
}
