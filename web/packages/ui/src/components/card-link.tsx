import { Slot } from "radix-ui";
import type { ReactNode } from "react";
import s from "./card-link.module.css";

// CardLink makes a whole card the target of the one link it holds (a figure that opens the list it counts): the
// keyboard sees one focus ring around the card, the pointer sees the card's outline deepen.
export function CardLink({ children }: { children: ReactNode }) {
  return <Slot.Root className={s.root}>{children}</Slot.Root>;
}
