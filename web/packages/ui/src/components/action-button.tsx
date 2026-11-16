import type { ComponentProps } from "react";
import { classes } from "../classes";
import s from "./action-button.module.css";
import { Glyph, type GlyphName } from "./glyph";

// ActionButton is the design's button: primary (at most one per view), secondary, ghost and danger, at the default
// 36px or small 32px height, with an optional icon before the words. A button with only an icon carries an aria-label.
// It is a button, never a link: a page that navigates uses a link.
export function ActionButton({
  variant = "sec",
  size = "md",
  icon,
  type = "button",
  children,
  ...props
}: {
  variant?: "pri" | "sec" | "gho" | "dan";
  size?: "md" | "sm";
  icon?: GlyphName;
  type?: "button" | "submit";
} & Omit<ComponentProps<"button">, "className" | "style" | "type">) {
  return (
    <button type={type} className={classes(s.root, s[variant], size === "sm" && s.sm)} {...props}>
      {icon ? <Glyph name={icon} /> : null}
      {children}
    </button>
  );
}
