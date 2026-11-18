import type { ReactNode } from "react";
import s from "./inline-error.module.css";

// InlineError says in a line, next to what failed, why it failed and that nothing changed; it is announced.
export function InlineError({ children }: { children: ReactNode }) {
  return (
    <span role="alert" className={s.root}>
      {children}
    </span>
  );
}
