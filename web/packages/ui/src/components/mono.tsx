import type { ReactNode } from "react";
import s from "./mono.module.css";

// Mono is an identifier or a name the system gives, in the monospaced face (the board's .mono); it may be long, so it
// wraps anywhere. title carries the whole value where the words shown are a shortening of it.
export function Mono({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className={s.root} title={title}>
      {children}
    </span>
  );
}
