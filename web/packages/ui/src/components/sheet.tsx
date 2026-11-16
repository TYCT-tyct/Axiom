import { type ReactNode, type RefObject, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { reducedMotion, settled } from "../motion";
import { ActionButton } from "./action-button";
import { Glyph } from "./glyph";
import s from "./sheet.module.css";

const focusable = "input:not(:disabled), select:not(:disabled), textarea:not(:disabled), button:not(:disabled), [href]";

export type SheetProps = {
  title: string;
  subtitle?: string;
  // side is the panel down the right edge (EditPanel); otherwise it is centred (CreateDialog). Below 600px both are
  // a sheet from the bottom.
  side?: boolean;
  // done marks a result: a check before the title and no close button, so only the footer's action ends it.
  done?: boolean;
  // dismissible lets Esc and a press on the backdrop close it: only while nothing would be lost or is on its way.
  dismissible: boolean;
  // locked disables every control in the body and the close button, while a write is out.
  locked?: boolean;
  // step changes when the body is replaced by the next one (form, then result), which enters with the step motion.
  step?: number;
  // onSubmit handles the form's submit (Enter, a submit button in the footer) and may close the sheet.
  onSubmit?: (close: () => void) => void;
  // start sits at the start of the footer (a destructive link); status is a line there that is read out as it changes.
  start?: ReactNode;
  status?: { text: string; saved?: boolean };
  // footer renders the actions; close plays the exit and then calls onClose.
  footer: (close: () => void) => ReactNode;
  onClose: () => void;
  children: ReactNode;
};

// Sheet is the modal surface every overlay of the console is drawn on: a native modal <dialog>, which makes the page
// behind it inert (Tab stays inside) with no script that injects styles, which the content security policy refuses.
// The parent mounts it while it is open. It takes the focus to its first control, keeps the page behind from
// scrolling, and gives the focus back to what had it when it goes. Closing plays the exit motion, then calls onClose.
export function Sheet({
  title,
  subtitle,
  side = false,
  done = false,
  dismissible,
  locked = false,
  step = 0,
  onSubmit,
  start,
  status,
  footer,
  onClose,
  children,
}: SheetProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const [leaving, setLeaving] = useState(false);
  const latest = useRef({ dismissible, leaving, onClose });
  latest.current = { dismissible, leaving, onClose };
  const close = () => {
    latest.current.leaving = true;
    setLeaving(true);
  };
  useModal(ref, leaving, latest);
  useStep(ref, body, step, side);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard path is the dialog's own Escape and the close button
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      tabIndex={-1}
      className={classes(s.root, side && s.side, leaving && s.leaving)}
      onCancel={(event) => {
        event.preventDefault();
        if (dismissible) {
          close();
        }
      }}
      onClose={() => {
        // A browser may close it without a cancelable cancel (a second Esc with no activation between): one that may
        // not close yet opens again.
        const now = latest.current;
        if (!now.leaving) {
          now.dismissible ? now.onClose() : ref.current?.showModal();
        }
      }}
      onClick={(event) => {
        // a press on the backdrop lands on the dialog itself; its content covers the rest of it
        if (dismissible && event.target === ref.current) {
          close();
        }
      }}
    >
      <form
        className={s.form}
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          // Once closing has begun nothing more is sent: what it would make could not be shown.
          if (!latest.current.leaving) {
            onSubmit?.(close);
          }
        }}
      >
        {/* closing locks everything at once, for the time the exit motion plays */}
        <fieldset className={s.lock} disabled={leaving}>
          <Head id={titleId} title={title} subtitle={subtitle} done={done} locked={locked} close={close} />
          <fieldset className={s.lock} disabled={locked}>
            <div key={step} ref={body} className={classes(s.body, step > 0 && s.stepped)}>
              {children}
            </div>
          </fieldset>
          <Foot start={start} status={status}>
            {footer(close)}
          </Foot>
        </fieldset>
      </form>
    </dialog>
  );
}

type Latest = { dismissible: boolean; leaving: boolean; onClose: () => void };

// useModal opens the dialog as a modal and keeps the page behind from scrolling while it is open; once leaving, it
// lets the exit motion play, closes it and calls onClose. On unmount it gives the focus back to what had it.
function useModal(ref: RefObject<HTMLDialogElement | null>, leaving: boolean, latest: RefObject<Latest>) {
  // A layout effect, so it is open before useStep moves the focus into it.
  useLayoutEffect(() => {
    const dialog = ref.current as HTMLDialogElement;
    const opener = document.activeElement as HTMLElement | null;
    const root = document.documentElement;
    const overflow = root.style.overflow;
    dialog.showModal();
    root.style.overflow = "hidden";
    return () => {
      root.style.overflow = overflow;
      // Closing here is the unmount, not a request to close; it also ends the modal state, so the opener can take
      // the focus back from inside the dialog (or from nowhere) before the dialog leaves the page.
      latest.current.leaving = true;
      dialog.close();
      const focused = document.activeElement;
      if (!focused || focused === document.body || dialog.contains(focused)) {
        opener?.focus();
      }
    };
  }, [ref, latest]);
  useEffect(() => {
    if (leaving) {
      const dialog = ref.current as HTMLDialogElement;
      settled(dialog).then(() => {
        dialog.close();
        latest.current.onClose();
      });
    }
  }, [ref, latest, leaving]);
}

// useStep moves the focus to the first control of the body each time a step replaces it, and grows or shrinks the
// centred box to its new height over the time the board animates it.
function useStep(
  ref: RefObject<HTMLDialogElement | null>,
  body: RefObject<HTMLDivElement | null>,
  step: number,
  side: boolean,
) {
  const height = useRef(0);
  useLayoutEffect(() => {
    const dialog = ref.current as HTMLDialogElement;
    (body.current?.querySelector<HTMLElement>(`[autofocus], ${focusable}`) ?? dialog).focus();
    const before = height.current;
    height.current = dialog.offsetHeight;
    if (step > 0 && !side && before > 0 && Math.abs(height.current - before) > 1 && !reducedMotion()) {
      dialog.animate([{ height: `${before}px` }, { height: `${height.current}px` }], {
        duration: 250,
        easing: "ease-out",
      });
    }
  }, [ref, body, step, side]);
}

// Foot holds the footer's actions, after an optional start and a status line read out as it changes.
function Foot({ start, status, children }: Pick<SheetProps, "start" | "status"> & { children: ReactNode }) {
  return (
    <div className={s.foot}>
      {start ? <div className={s.start}>{start}</div> : null}
      {status ? (
        <span role="status" className={classes(s.status, status.saved && s.saved)}>
          {status.text}
        </span>
      ) : null}
      {children}
    </div>
  );
}

// Head is the title with its line, and the close button, or on a result the check in its place.
function Head({
  id,
  title,
  subtitle,
  done,
  locked,
  close,
}: {
  id: string;
  title: string;
  subtitle?: string;
  done: boolean;
  locked: boolean;
  close: () => void;
}) {
  const { t } = useTranslation("common");
  return (
    <div className={s.head}>
      {done ? (
        <span className={s.ok}>
          <Glyph name="check" />
        </span>
      ) : null}
      <div className={s.heading}>
        <h2 id={id} className={s.title}>
          {title}
        </h2>
        {subtitle ? <p className={s.subtitle}>{subtitle}</p> : null}
      </div>
      {done ? null : (
        <button type="button" className={s.close} aria-label={t("dialog.close")} disabled={locked} onClick={close}>
          <Glyph name="close" />
        </button>
      )}
    </div>
  );
}

// SubmitActions is a form's footer: Cancel, and the submit button, which while the request is out is disabled with a
// spinner and its busy words, and after a failure offers to try again.
export function SubmitActions({
  label,
  busy,
  saving,
  failed,
  close,
}: {
  label: string;
  busy: string;
  saving: boolean;
  failed: boolean;
  close: () => void;
}) {
  const { t } = useTranslation("common");
  return (
    <>
      <ActionButton disabled={saving} onClick={close}>
        {t("cancel")}
      </ActionButton>
      <ActionButton variant="pri" type="submit" disabled={saving}>
        {saving ? <span aria-hidden="true" className={s.spin} /> : null}
        {saving ? busy : failed ? t("overlay.retry") : label}
      </ActionButton>
    </>
  );
}
