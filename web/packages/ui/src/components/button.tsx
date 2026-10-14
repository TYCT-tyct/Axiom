import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "../cn";

// Button in the shadcn/ui pattern: variants by class-variance-authority, asChild to style a link as a button. At most
// one primary per screen; an icon-only button must carry an aria-label.
export const buttonVariants = cva(
  "inline-flex shrink-0 cursor-pointer items-center justify-center gap-2 rounded-btn font-medium whitespace-nowrap transition-[background-color,color,scale] duration-150 ease-out select-none active:scale-[0.97] disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        primary: "bg-btn text-btn-ink hover:opacity-90",
        secondary: "border border-b2 bg-s2 text-t1 shadow-panel hover:border-b3",
        ghost: "text-t2 hover:bg-s2 hover:text-t1",
      },
      size: {
        lg: "h-11 px-5 text-[15px]",
        md: "h-9 px-4 text-sm",
        sm: "h-8 px-3 text-ui",
        icon: "size-8",
      },
    },
    defaultVariants: { variant: "secondary", size: "sm" },
  },
);

export function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: ComponentProps<"button"> & VariantProps<typeof buttonVariants> & { asChild?: boolean }) {
  const Component = asChild ? Slot.Root : "button";
  return (
    <Component
      data-slot="button"
      className={cn(buttonVariants({ variant, size }), className)}
      {...(asChild ? {} : { type: "button" })}
      {...props}
    />
  );
}
