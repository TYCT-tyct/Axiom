import { type ReactNode, useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { ActionButton } from "./action-button";
import s from "./dialog.module.css";

// Dialog is a modal over the page, a native <dialog>: it traps focus, closes on Escape and hands focus back by itself,
// and needs no script that injects styles, which the content security policy blocks. The parent mounts it while it is
// open, so a form inside starts fresh each time. A dialog that is not dismissible ignores Escape and the backdrop and
// has no close button: it holds something the user must act on, such as a key that is shown once.
export function Dialog({
  title,
  onClose,
  dismissible = true,
  children,
}: {
  title: string;
  onClose: () => void;
  dismissible?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    if (ref.current && !ref.current.open) {
      ref.current.showModal();
    }
  }, []);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard path is the dialog's own Escape and the close button
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onClose={onClose}
      onCancel={(event) => {
        if (!dismissible) {
          event.preventDefault();
        }
      }}
      onClick={(event) => {
        // a click on the backdrop lands on the dialog itself, not on its content
        if (dismissible && event.target === ref.current) {
          ref.current?.close();
        }
      }}
      className={s.root}
    >
      <div className={s.body}>
        <div className={s.head}>
          <h2 id={titleId} className={s.title}>
            {title}
          </h2>
          {dismissible ? (
            <ActionButton
              variant="gho"
              size="sm"
              icon="close"
              aria-label={t("dialog.close")}
              onClick={() => ref.current?.close()}
            />
          ) : null}
        </div>
        {children}
      </div>
    </dialog>
  );
}
