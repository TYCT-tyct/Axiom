import type { ReactNode } from "react";
import { Card } from "./card";
import type { IconName } from "./icon";
import { Tag } from "./tag";

// Panel and Badge under the names the pages were written with (#747): a Panel is a card whose content runs edge to
// edge (a table, a list of facts), a Badge a tag. Pages move to Card and Tag as they are restyled, and this file goes
// with the last of them.
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

export function Badge({
  tone = "neutral",
  icon,
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "bad";
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <Tag tone={tone} icon={icon}>
      {children}
    </Tag>
  );
}
