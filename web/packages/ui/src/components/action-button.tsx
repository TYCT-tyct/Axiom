import { Slot } from "radix-ui";
import type { ComponentProps, ReactNode } from "react";
import { classes } from "../classes";
import s from "./action-button.module.css";
import { Glyph, type GlyphName, glyphNames } from "./glyph";
import { Icon, type IconName } from "./icon";

// ActionButton is the design's button: primary (at most one per view), secondary, ghost and danger, at the default
// 36px or small 32px height, with an optional icon before the words. A button with only an icon is square and carries
// an aria-label. asChild lends the look to the one child, a router link that leads somewhere, as a button never does.
// pending refuses a second press while a request is out, so a slow answer never ends in two charges.
export function ActionButton({
  variant = "sec",
  size = "md",
  icon,
  type = "button",
  asChild = false,
  pending = false,
  square = false,
  wide = false,
  disabled,
  children,
  ...props
}: {
  variant?: "pri" | "sec" | "gho" | "dan";
  size?: "md" | "sm";
  icon?: GlyphName | IconName;
  type?: "button" | "submit";
  asChild?: boolean;
  pending?: boolean;
  // square marks a link that holds only an icon (asChild), which the button cannot tell from its children.
  square?: boolean;
  // wide fills its row, as a form's one submit button does on a narrow page.
  wide?: boolean;
  children?: ReactNode;
} & Omit<ComponentProps<"button">, "className" | "style" | "type" | "children">) {
  const className = classes(
    s.root,
    s[variant],
    size === "sm" && s.sm,
    (square || !children) && s.square,
    pending && s.pending,
  );
  if (asChild) {
    return <Slot.Root className={className}>{children}</Slot.Root>;
  }
  return (
    <button
      type={type}
      className={className}
      disabled={pending || disabled}
      aria-busy={pending || undefined}
      {...props}
    >
      {icon ? <ButtonIcon name={icon} /> : null}
      {children}
    </button>
  );
}

function ButtonIcon({ name }: { name: GlyphName | IconName }) {
  return (glyphNames as string[]).includes(name) ? (
    <Glyph name={name as GlyphName} />
  ) : (
    <Icon name={name as IconName} />
  );
}

// Button and SubmitButton under the names and variants the pages were written with (#747). Pages move to ActionButton
// as they are restyled, and these go with the last of them.
const variants = { primary: "pri", secondary: "sec", ghost: "gho", danger: "dan" } as const;

export function Button({
  variant = "secondary",
  size = "sm",
  asChild = false,
  wide = false,
  type = "button",
  ...props
}: Omit<ComponentProps<"button">, "style" | "className"> & {
  variant?: keyof typeof variants | null;
  size?: "lg" | "md" | "sm" | "icon" | null;
  asChild?: boolean;
  wide?: boolean;
}) {
  const look = classes(
    s.root,
    s[variants[variant ?? "secondary"]],
    (size === "sm" || size === "icon") && s.sm,
    size === "icon" && s.square,
    wide && s.wide,
  );
  return asChild ? <Slot.Root className={look} {...props} /> : <button type={type} className={look} {...props} />;
}

export function SubmitButton({
  pending,
  disabled,
  children,
  ...props
}: Omit<ComponentProps<typeof Button>, "type" | "disabled"> & { pending: boolean; disabled?: boolean }) {
  return (
    <Button
      variant="primary"
      size="md"
      {...props}
      type="submit"
      disabled={pending || disabled}
      aria-busy={pending || undefined}
    >
      {children}
    </Button>
  );
}
