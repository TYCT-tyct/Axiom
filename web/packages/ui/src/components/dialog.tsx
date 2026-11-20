import { type ReactNode, useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { board } from "../board-classes";
import { classes } from "../classes";
import { ActionButton } from "./action-button";
import s from "./dialog.module.css";
import { Icon } from "./icon";

type SaveState = "idle" | "saved" | "error";

// ModalShell is the native <dialog> engine shared by Dialog and SidePanel (ppDlg in AxApp.logic.html, .nkdlg and
// .nkdlg.side in AxApp/regions.css): it traps focus, closes on Escape and backdrop click, and restores focus natively.
function ModalShell({
  title,
  subtitle,
  side = false,
  dismissible = true,
  onClose,
  footer,
  children,
}: {
  title: string;
  subtitle?: string;
  side?: boolean;
  dismissible?: boolean;
  onClose: () => void;
  footer?: (close: () => void) => ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal();
  }, []);
  const close = () => ref.current?.close();
  return (
    <div className={classes(board.board, board.ax, board.ov, s.host)}>
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard path is the dialog's own Escape and the close button */}
      <dialog
        ref={ref}
        aria-labelledby={titleId}
        onClose={onClose}
        onCancel={(event) => {
          if (!dismissible) event.preventDefault();
        }}
        onClick={(event) => {
          // a click on the backdrop lands on the dialog itself, not on its content
          if (dismissible && event.target === ref.current) close();
        }}
        className={classes(board.nkdlg, side && board.side, s.root)}
      >
        <div className={board.nkh}>
          <div>
            <h2 id={titleId}>{title}</h2>
            {subtitle ? <p>{subtitle}</p> : null}
          </div>
          {dismissible ? (
            <button type="button" className={board.nkx} aria-label={t("dialog.close")} onClick={close}>
              <Icon name="close" />
            </button>
          ) : null}
        </div>
        <div className={classes(board.nkb, !side && s.body)}>{children}</div>
        {footer ? <div className={board.nkfoot}>{footer(close)}</div> : null}
      </dialog>
    </div>
  );
}

// Dialog is a centred modal over the page. The parent mounts it while open, so a form inside starts fresh each time. A
// non-dismissible dialog ignores Escape and the backdrop and hides its close button.
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
  return (
    <ModalShell title={title} dismissible={dismissible} onClose={onClose}>
      {children}
    </ModalShell>
  );
}

// SidePanel is the board's right-hand detail and live-edit panel (ppDlg with side:true in AxApp.logic.html), with an
// optional live-save status indicator and bottom-sheet layout on narrow viewports.
export function SidePanel({
  title,
  subtitle,
  onClose,
  dismissible = true,
  saveState,
  onRetrySave,
  footerLeft,
  doneLabel,
  footer,
  children,
}: {
  title: string;
  subtitle?: string;
  onClose: () => void;
  dismissible?: boolean;
  saveState?: SaveState;
  onRetrySave?: () => void;
  footerLeft?: ReactNode;
  doneLabel?: string;
  footer?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  return (
    <ModalShell
      title={title}
      subtitle={subtitle}
      side
      dismissible={dismissible}
      onClose={onClose}
      footer={(close) =>
        footer ?? (
          <>
            {footerLeft ? <div className={board.ppdel}>{footerLeft}</div> : null}
            {saveState ? <SaveStatus state={saveState} onRetry={onRetrySave} /> : null}
            <ActionButton variant="pri" onClick={close}>
              {doneLabel ?? t("sidePanel.done")}
            </ActionButton>
          </>
        )
      }
    >
      {children}
    </ModalShell>
  );
}

function SaveStatus({ state, onRetry }: { state: SaveState; onRetry?: () => void }) {
  const { t } = useTranslation("common");
  const label =
    state === "saved" ? t("sidePanel.saved") : state === "error" ? t("sidePanel.saveFailed") : t("sidePanel.liveHint");
  return (
    <span
      role={state === "error" ? "alert" : "status"}
      className={classes(board.ppsaved, state === "saved" && board.on, state === "error" && board.bad)}
    >
      {label}
      {state === "error" && onRetry ? (
        <button type="button" onClick={onRetry}>
          {t("state.retry")}
        </button>
      ) : null}
    </span>
  );
}
