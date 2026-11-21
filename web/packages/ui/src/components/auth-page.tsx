import type { ReactNode } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./auth-page.module.css";
import { Logo } from "./logo";
import { usePageTitle } from "./page-title";
import { PreferenceSwitches } from "./preferences";

// AuthPage is the page around a sign-in or two-step form of an app without a sign-in board (the admin): the site's
// name and logo and the preference switches above, then one narrow column, the form's heading, a line under it, the
// form and a footer. It is its own page (main, heading, title), as the pages inside an app's shell are.
export function AuthPage({
  title,
  description,
  footer,
  name,
  logoUrl,
  children,
}: {
  title: string;
  description?: string;
  footer?: ReactNode;
  name?: string;
  logoUrl?: string;
  children: ReactNode;
}) {
  usePageTitle(title);
  return (
    <div className={classes(board.board, board.ax, board.ov, s.root)}>
      <header className={s.top}>
        <Logo name={name} logoUrl={logoUrl} className={s.logo} />
        <PreferenceSwitches className={s.switches} />
      </header>
      <main id="main" tabIndex={-1} className={s.main}>
        <div className={s.head}>
          <h1 className={s.title}>{title}</h1>
          {description ? <p className={s.description}>{description}</p> : null}
        </div>
        {children}
        {footer ? <p className={s.footer}>{footer}</p> : null}
      </main>
    </div>
  );
}
