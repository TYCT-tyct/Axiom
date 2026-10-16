import { type ReactNode, useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./button";
import { Icon } from "./icon";

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
      className="m-auto max-h-[calc(100dvh-2rem)] w-[min(32rem,calc(100vw-2rem))] overflow-y-auto rounded-frame bg-s1 p-0 text-ui text-t1 shadow-modal backdrop:bg-scrim"
    >
      <div className="space-y-4 p-5">
        <div className="flex items-start justify-between gap-3">
          <h2 id={titleId} className="min-w-0 text-[15px] text-t1">
            {title}
          </h2>
          {dismissible ? (
            <Button
              variant="ghost"
              size="icon"
              className="-mt-1 -mr-2"
              aria-label={t("dialog.close")}
              onClick={() => ref.current?.close()}
            >
              <Icon name="close" />
            </Button>
          ) : null}
        </div>
        {children}
      </div>
    </dialog>
  );
}
