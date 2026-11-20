import type { ReactNode } from "react";
import s from "./announcement.module.css";

// Announcement is one announcement an administrator wrote: a title and the text under it, both keeping the line
// breaks they were written with.
export function Announcement({ title, children }: { title: string; children: ReactNode }) {
  return (
    <article className={s.root}>
      <h2>{title}</h2>
      {children}
    </article>
  );
}
