import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./nav-group.module.css";

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
    <div role="group" aria-label={label} className={classes(s.root, tiles && s.tiles)}>
      {showLabel ? (
        <div className={s.label} aria-hidden="true">
          {label}
        </div>
      ) : null}
      {children}
    </div>
  );
}
