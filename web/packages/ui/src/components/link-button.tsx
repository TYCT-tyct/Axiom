import type { ReactNode } from "react";
import { TextLink } from "./text-link";

// LinkButton is a button that reads as a link (the admin's .adlink): it does something on this page (opens a row's
// drawer, a dialog) rather than going to an address. label names it for a screen reader where the words shown are only
// a name in a list.
export function LinkButton({
  children,
  label,
  disabled = false,
  onClick,
}: {
  children: ReactNode;
  label?: string;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <TextLink>
      <button type="button" aria-label={label} disabled={disabled} onClick={onClick}>
        {children}
      </button>
    </TextLink>
  );
}
