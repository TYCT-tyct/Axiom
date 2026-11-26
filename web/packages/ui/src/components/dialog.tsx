import { type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { board } from "../board-classes";
import { classes } from "../classes";
import { ActionButton } from "./action-button";
import s from "./dialog.module.css";
import { Icon } from "./icon";

type SaveState = "idle" | "saved" | "error";

// The classes one kind of modal draws with: the board's dialog and side panel (.nkdlg, .nkdlg.side), or the admin
// board's detail drawer (.qdr.adqd in admin_style.css), whose rules live in this module.
type Look = Record<"root" | "out" | "head" | "close" | "body" | "foot", string | undefined>;

const boardLook = (side: boolean): Look => ({
  root: classes(board.nkdlg, side && board.side),
  out: board.out,
  head: board.nkh,
  close: board.nkx,
  body: classes(board.nkb, !side && s.body),
  foot: board.nkfoot,
});

const drawerLook = (wide: boolean): Look => ({
  root: classes(s.drawer, wide && s.wide),
  out: s.drawerOut,
  head: s.drawerHead,
  close: s.iconButton,
  body: s.drawerBody,
  foot: s.drawerFoot,
});

// ModalShell is the native <dialog> engine shared by Dialog and SidePanel (ppDlg in AxApp.logic.html, .nkdlg and
// .nkdlg.side in AxApp/regions.css): it traps focus, closes on Escape and backdrop click, and restores focus natively.
// Closing plays the board's way out first (.nkdlg.out: the dialog fades and sinks, the side panel slides off), and the
// backdrop fades with it; onClose follows when the dialog has closed.
function ModalShell({
  title,
  subtitle,
  look,
  drawer = false,
  dismissible = true,
  onClose,
  onKeyDown,
  headExtra,
  footer,
  children,
}: {
  title: string;
  subtitle?: string;
  look: Look;
  // A drawer is a page's detail kept in its address, which the page leaves open as a navigation resolves (focusPage).
  drawer?: boolean;
  dismissible?: boolean;
  onClose: () => void;
  onKeyDown?: (event: KeyboardEvent<HTMLDialogElement>) => void;
  headExtra?: ReactNode;
  footer?: (close: () => void) => ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  // On the way out: "start" lets the way in go (data-leaving), "out" plays the board's way out.
  const [leaving, setLeaving] = useState<"start" | "out" | null>(null);
  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal();
  }, []);
  useEffect(() => {
    const dialog = ref.current;
    if (!leaving || !dialog) return;
    if (leaving === "start") {
      // A transition never starts on a property an animation holds, and the way in (nkIn, ppSide, drawer-in) holds
      // opacity and transform to its end: it is let go and its end style read before the way out is put on.
      void getComputedStyle(dialog).opacity;
      setLeaving("out");
      return;
    }
    // A DOM without the Web Animations API (the unit tests') has nothing to play and closes at once.
    const playing = dialog.getAnimations?.() ?? [];
    void Promise.allSettled(playing.map((animation) => animation.finished)).then(() => dialog.close());
  }, [leaving]);
  const close = () => setLeaving((now) => now ?? "start");
  return (
    <div className={classes(board.board, board.ax, board.ov, s.host)}>
      <dialog
        ref={ref}
        aria-labelledby={titleId}
        onClose={onClose}
        onCancel={(event) => {
          event.preventDefault();
          if (dismissible) close();
        }}
        onClick={(event) => {
          // a click on the backdrop lands on the dialog itself, not on its content
          if (dismissible && event.target === ref.current) close();
        }}
        onKeyDown={onKeyDown}
        className={classes(look.root, s.root, leaving === "out" && look.out)}
        data-leaving={leaving ? "" : undefined}
        data-drawer={drawer ? "" : undefined}
      >
        <div className={look.head}>
          <div>
            <h2 id={titleId}>{title}</h2>
            {subtitle ? <p>{subtitle}</p> : null}
          </div>
          {headExtra}
          {dismissible ? (
            <button type="button" className={look.close} aria-label={t("dialog.close")} onClick={close}>
              <Icon name="close" />
            </button>
          ) : null}
        </div>
        <div className={look.body}>{children}</div>
        {footer ? <div className={look.foot}>{footer(close)}</div> : null}
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
    <ModalShell title={title} look={boardLook(false)} dismissible={dismissible} onClose={onClose}>
      {children}
    </ModalShell>
  );
}

// PanelSteps lets a panel opened from a list walk that list without closing (adPanel's "2 / 6" in admin_core.js): the
// previous and next buttons, and J/K or the up and down keys while focus is not in a field. index counts from 0, and
// onStep gets -1 or 1, never past either end; the parent swaps the panel's content for that row.
export type PanelSteps = {
  index: number;
  count: number;
  onStep: (by: -1 | 1) => void;
  previousLabel: string;
  nextLabel: string;
};

const stepKeys: Partial<Record<string, -1 | 1>> = { ArrowDown: 1, j: 1, ArrowUp: -1, k: -1 };

const typing = (target: EventTarget) =>
  target instanceof HTMLElement && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));

// SidePanel is the board's right-hand detail and live-edit panel (ppDlg with side:true in AxApp.logic.html), with an
// optional live-save status indicator and bottom-sheet layout on narrow viewports. drawer draws it as the admin board's
// detail drawer instead (adPanel in admin_core.js): on the right over the list, 480px wide (440px under 1024px, 600px
// when wide), a bottom sheet under 600px, and a footer only when one is given.
export function SidePanel({
  title,
  subtitle,
  onClose,
  dismissible = true,
  drawer = false,
  wide = false,
  steps,
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
  drawer?: boolean;
  wide?: boolean;
  steps?: PanelSteps;
  saveState?: SaveState;
  onRetrySave?: () => void;
  footerLeft?: ReactNode;
  doneLabel?: string;
  footer?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const step = (by: -1 | 1) => {
    if (steps && steps.index + by >= 0 && steps.index + by < steps.count) steps.onStep(by);
  };
  const boardFooter = (close: () => void) =>
    footer ?? (
      <>
        {footerLeft ? <div className={board.ppdel}>{footerLeft}</div> : null}
        {saveState ? <SaveStatus state={saveState} onRetry={onRetrySave} /> : null}
        <ActionButton variant="pri" onClick={close}>
          {doneLabel ?? t("sidePanel.done")}
        </ActionButton>
      </>
    );
  return (
    <ModalShell
      title={title}
      subtitle={subtitle}
      look={drawer ? drawerLook(wide) : boardLook(true)}
      drawer={drawer}
      dismissible={dismissible}
      onClose={onClose}
      onKeyDown={(event) => {
        const by = stepKeys[event.key];
        if (!steps || !by || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
        if (typing(event.target)) return;
        event.preventDefault();
        step(by);
      }}
      headExtra={steps && steps.count > 1 ? <StepNav steps={steps} onStep={step} /> : null}
      footer={drawer ? (footer ? () => footer : undefined) : boardFooter}
    >
      {children}
    </ModalShell>
  );
}

// The end buttons are aria-disabled, not disabled: a button disabled while it holds focus drops focus out of the
// dialog, and the keys would stop reaching it.
function StepNav({ steps, onStep }: { steps: PanelSteps; onStep: (by: -1 | 1) => void }) {
  return (
    <span className={s.steps}>
      <span className={s.count}>{`${steps.index + 1} / ${steps.count}`}</span>
      <button
        type="button"
        className={s.iconButton}
        aria-label={steps.previousLabel}
        aria-disabled={steps.index <= 0}
        onClick={() => onStep(-1)}
      >
        <Icon name="up" />
      </button>
      <button
        type="button"
        className={s.iconButton}
        aria-label={steps.nextLabel}
        aria-disabled={steps.index >= steps.count - 1}
        onClick={() => onStep(1)}
      >
        <Icon name="down" />
      </button>
    </span>
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
