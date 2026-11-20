import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./page-frame.module.css";

// PageFrame gives a page drawn by a board slice the column and padding the board's page has, until the shell does
// (docs/frontend.md「前端工程」). board is for a slice that scopes its rules with its board's own frame classes: the frame
// then carries them, and is as wide as the window instead of a column of its own.
export function PageFrame({ board = false, children }: { board?: boolean; children: ReactNode }) {
  return board ? (
    <div className={s.board}>
      <div className={classes(s.page, s.inner)}>{children}</div>
    </div>
  ) : (
    <div className={classes(s.page, s.column)}>{children}</div>
  );
}
