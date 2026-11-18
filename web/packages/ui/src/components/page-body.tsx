import type { ReactNode } from "react";
import s from "./page-body.module.css";

// PageBody is the area under the bars that a page without a board of its own fills (the board's .qpages): the console's
// width and margins, its blocks 16px apart.
export function PageBody({ children }: { children: ReactNode }) {
  return <div className={s.root}>{children}</div>;
}
