import { type ReactNode, useRef, useState } from "react";
import CopyRegion from "./AxApp/regions/AdminCopyButton.tsx";
import DangerRegion from "./AxApp/regions/AdminDangerButton.tsx";
import LinkRegion from "./AxApp/regions/AdminLinkButton.tsx";
import PrimaryRegion from "./AxApp/regions/AdminPrimaryButton.tsx";
import SecondaryRegion from "./AxApp/regions/AdminSecondaryButton.tsx";

const actions = { primary: PrimaryRegion, secondary: SecondaryRegion, danger: DangerRegion, link: LinkRegion };

export function ActionButton({
  children,
  variant = "secondary",
  type = "button",
  disabled,
  busy,
  label,
  describedBy,
  onClick,
}: {
  children: ReactNode;
  variant?: keyof typeof actions;
  type?: "button" | "submit" | "reset";
  disabled?: boolean;
  busy?: boolean;
  label?: string;
  describedBy?: string;
  onClick?: () => void;
}) {
  const Region = actions[variant];
  const blocked = disabled || busy;
  return (
    <Region
      t={String}
      label={children}
      values={{
        disabled: blocked,
        disabledClass: blocked ? (variant === "link" ? "btn dis" : "dis") : "",
        press: onClick,
        button: (node) => {
          if (!(node instanceof HTMLButtonElement)) return;
          node.type = type;
          if (label) node.setAttribute("aria-label", label);
          else node.removeAttribute("aria-label");
          if (describedBy) node.setAttribute("aria-describedby", describedBy);
          else node.removeAttribute("aria-describedby");
          if (busy) node.setAttribute("aria-busy", "true");
          else node.removeAttribute("aria-busy");
        },
      }}
    />
  );
}

type CopyState = "copying" | "copied" | "failed";

export function CopyButton({
  text,
  label,
  copied,
  failed,
}: {
  text: string;
  label: string;
  copied: string;
  failed: string;
}) {
  const [last, setLast] = useState<{ text: string; state: CopyState }>();
  const sequence = useRef(0);
  const state = last?.text === text ? last.state : undefined;
  const copy = async () => {
    if (state === "copying") return;
    const attempt = ++sequence.current;
    setLast({ text, state: "copying" });
    try {
      await navigator.clipboard.writeText(text);
      if (sequence.current === attempt) setLast({ text, state: "copied" });
    } catch {
      if (sequence.current === attempt) setLast({ text, state: "failed" });
    }
  };
  return (
    <span aria-live="polite">
      <CopyRegion
        t={String}
        label={state === "copied" ? copied : state === "failed" ? failed : label}
        values={{
          tone: state === "copied" ? "sec" : state === "copying" ? "pri dis" : "pri",
          copy: () => void copy(),
          button: (node) => {
            if (state === "copying") node?.setAttribute("aria-busy", "true");
            else node?.removeAttribute("aria-busy");
          },
        }}
      />
    </span>
  );
}
