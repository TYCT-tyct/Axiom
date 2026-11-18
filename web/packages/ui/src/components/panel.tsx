import type { ReactNode } from "react";
import { Card } from "./card";

// Panel under the name the pages were written with (#747): a card whose content runs edge to edge (a table, a list of
// facts). Pages move to Card as they are restyled, and this file goes with the last of them.
export function Panel({
  id,
  title,
  action,
  children,
}: {
  id?: string;
  title?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card id={id} title={title} actions={action} layout="flush">
      {children}
    </Card>
  );
}
