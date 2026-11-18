import { type FormEventHandler, type ReactNode, useEffectEvent, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import DialogRegion from "./AxApp/regions/PartDialog.tsx";
import TextAreaRegion from "./AxApp/regions/PartTextAreaField.tsx";
import TextRegion from "./AxApp/regions/PartTextField.tsx";

type Action = {
  label: string;
  disabled?: boolean;
  onClick: () => void;
};

const tabStops = (panel: HTMLElement) =>
  Array.from(panel.querySelectorAll<HTMLElement>("button, input, select, textarea, a[href], [tabindex]")).filter(
    (el) => el.tabIndex >= 0 && !el.matches(":disabled") && el.getClientRects().length > 0,
  );

function trapTab(event: KeyboardEvent, dialog: HTMLElement) {
  const targets = tabStops(dialog);
  const active = document.activeElement as HTMLElement;
  const boundary = event.shiftKey ? targets[0] : targets.at(-1);
  if (active !== boundary && targets.includes(active)) return;
  event.preventDefault();
  ((event.shiftKey ? targets.at(-1) : targets[0]) ?? dialog).focus();
}

function useDialogFocus(onClose: () => void) {
  const panel = useRef<HTMLElement>(null);
  const [host] = useState(() => document.createElement("div"));

  const handleKeys = useEffectEvent((event: KeyboardEvent) => {
    const dialog = panel.current;
    if (!dialog || host.closest("[inert]") || event.defaultPrevented) return;
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key === "Tab") trapTab(event, dialog);
  });

  useLayoutEffect(() => {
    const trigger = document.activeElement;
    let background: Element[] = [];
    const attach = () => {
      const frame = document.querySelector(".ax .fr") ?? document.body;
      if (frame === host.parentElement) return;
      for (const el of background) el.removeAttribute("inert");
      frame.append(host);
      background = Array.from(frame.children).filter((el) => el !== host && !el.hasAttribute("inert"));
      for (const el of background) el.setAttribute("inert", "");
      const dialog = panel.current;
      if (dialog) {
        const targets = tabStops(dialog);
        (targets.find((el) => el.matches("input, textarea, select")) ?? targets[0] ?? dialog).focus();
      }
    };
    attach();
    const keys = (event: KeyboardEvent) => handleKeys(event);
    const focusInside = (event: FocusEvent) => {
      if (host.contains(event.target as Node) || host.closest("[inert]")) return;
      const dialog = panel.current;
      if (dialog) (tabStops(dialog)[0] ?? dialog).focus();
    };
    document.addEventListener("keydown", keys);
    document.addEventListener("focusin", focusInside);
    // The shell can swap artboards while the page stays mounted.
    const observer = new MutationObserver(attach);
    observer.observe(document.body, { childList: true, subtree: true });
    return () => {
      document.removeEventListener("keydown", keys);
      document.removeEventListener("focusin", focusInside);
      observer.disconnect();
      for (const el of background) el.removeAttribute("inert");
      host.remove();
      if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus({ preventScroll: true });
    };
  }, [host]);

  useLayoutEffect(() => {
    const dialog = panel.current;
    if (dialog?.isConnected && !host.closest("[inert]") && !dialog.contains(document.activeElement)) dialog.focus();
  });
  return { panel, host };
}

export function Dialog({
  title,
  description,
  closeLabel,
  onClose,
  children,
  primary,
  secondary,
  keyboardHint,
}: {
  title: string;
  description?: string;
  closeLabel: string;
  onClose: () => void;
  children: ReactNode;
  primary: Omit<Action, "onClick"> &
    ({ onClick: () => void; form?: never } | { form: string; onClick?: never }) & { danger?: boolean; busy?: boolean };
  secondary?: Action;
  keyboardHint?: string;
}) {
  const id = useId();
  const { panel, host } = useDialogFocus(onClose);
  return createPortal(
    <DialogRegion
      t={String}
      text={{
        title,
        description: description ?? "",
        closeLabel,
        primary: primary.label,
        secondary: secondary?.label ?? "",
        keyboardHint: keyboardHint ?? "",
      }}
      values={{
        dialog: (node) => {
          panel.current = node as HTMLElement | null;
          if (node instanceof HTMLDialogElement) node.open = true;
        },
        titleId: id,
        close: onClose,
        hideDescription: !description,
        hideSecondary: !secondary,
        hideHint: !keyboardHint,
        primaryClass: primary.danger ? "sec dan2" : "pri",
        primary: primary.onClick,
        form: primary.form,
        primaryButton: (node) => {
          if (!(node instanceof HTMLButtonElement)) return;
          node.type = primary.form ? "submit" : "button";
          if (primary.busy) node.setAttribute("aria-busy", "true");
          else node.removeAttribute("aria-busy");
        },
        primaryDisabled: primary.disabled,
        secondary: secondary?.onClick,
        secondaryDisabled: secondary?.disabled,
        // Match the board's unpadded div and anchor its overlay to the admin's scrolling viewport.
        placement: "position: fixed; border: 0; margin: 0; padding: 0; color: inherit",
      }}
    >
      {children}
    </DialogRegion>,
    host,
  );
}

type Field = {
  label: string;
  name: string;
  hint?: string;
  error?: string;
  defaultValue?: string;
  placeholder?: string;
  maxLength?: number;
  onInput?: FormEventHandler<Element>;
};

function useField({ label, name, hint, error, defaultValue, placeholder, maxLength, onInput }: Field) {
  const id = useId();
  const message = error ?? hint;
  return {
    t: String,
    text: { label, message: message ?? "" },
    values: {
      id,
      name,
      initial: defaultValue,
      placeholder,
      descriptionId: `${id}-message`,
      describedBy: message ? `${id}-message` : undefined,
      hideMessage: !message,
      messageClass: error ? "nkerr" : "nkhint",
      invalidClass: error ? "bad" : "",
      input: onInput,
      control: (node: Element | null) => {
        if (!node) return;
        if (error) node.setAttribute("aria-invalid", "true");
        else node.removeAttribute("aria-invalid");
        if (maxLength !== undefined) (node as HTMLInputElement | HTMLTextAreaElement).maxLength = maxLength;
      },
    },
  };
}

export function TextField({ type = "text", autoComplete, ...field }: Field & { type?: string; autoComplete?: string }) {
  const props = useField(field);
  return <TextRegion {...props} values={{ ...props.values, inputType: type, autocomplete: autoComplete }} />;
}

export function TextAreaField(field: Field) {
  return <TextAreaRegion {...useField(field)} />;
}
