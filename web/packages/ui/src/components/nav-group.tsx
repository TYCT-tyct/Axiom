import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./nav-group.module.css";

// NavGroup is one named group of a navigation (adNavHtml's .adnvg and .adng in admin_core.js): its name above its
// links, or, when the name is not shown (the icon rail), a thin line between it and the group before. tiles lays the
// links out three to a row, as the phone's more sheet does.
export function NavGroup({
  label,
  showLabel = true,
  tiles = false,
  children,
}: {
  label: string;
  showLabel?: boolean;
  tiles?: boolean;
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/useSemanticElements: A navigation group has links and no form controls.
    <div
      role="group"
      aria-label={label}
      className={classes(s.root, tiles && s.tiles)}
      data-line={showLabel ? undefined : ""}
    >
      <div className={s.label} aria-hidden="true">
        {label}
      </div>
      {children}
    </div>
  );
}
