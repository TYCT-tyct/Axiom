import { Gate, useEndPending, useErrors, useSignOut } from "@axiom/app";
import LoginAction from "@axiom/boards/Login/regions/LoginAction";
import LoginError from "@axiom/boards/Login/regions/LoginError";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import { type ComponentProps, type KeyboardEvent, type ReactNode, useLayoutEffect } from "react";
import { useTranslation } from "react-i18next";
import { pressKey } from "../board";
import type { Attempt } from "./attempt";

// The parts the sign-in pages' steps share, made of the Login board's parts.

// LoginGate is a sign-in page's gate (@axiom/app Gate), with the page set in the faces cut for the sign-in pages
// (web/scripts/fonts/sources.json: Axiom CJK): what shows before the board (the gate's loading and failure states) as
// well as the board. The console's own body face is cut for its other pages, and would load a second Chinese face
// here. The family is the root's --family-ui (console/src/main.css), changed while the page is shown, as the brand's
// colours are (useBrandStyle); a layout effect, so the first frame has it.
export function LoginGate({ stage, children }: { stage: ComponentProps<typeof Gate>["stage"]; children: ReactNode }) {
  useLayoutEffect(() => {
    const root = document.documentElement;
    root.style.setProperty("--family-ui", "var(--family-site)");
    return () => {
      root.style.removeProperty("--family-ui");
    };
  }, []);
  return <Gate stage={stage}>{children}</Gate>;
}

// FormProblem is a step's error line, announced as it appears; id lets the field it is about point at it.
export function FormProblem({ id, message }: { id?: string; message: ReactNode }) {
  return message ? <LoginError t={String} text={{ message: String(message) }} values={{ id: id ?? "" }} /> : null;
}

// NoteAction is a note under a step whose link does something on this page (go back a step, sign out, try again).
export function NoteAction({ label, onPress }: { label: string; onPress: () => void }) {
  return (
    <LoginNote t={String}>
      <LoginAction
        t={String}
        label={label}
        values={{
          onPress: (event) => {
            event.preventDefault();
            onPress();
          },
          onKeyDown: (event) => pressKey(event as KeyboardEvent<Element>),
        }}
      />
    </LoginNote>
  );
}

// SignOutNote ends a session that stopped halfway (a pending second step, terms not yet accepted), to start again as
// someone else or as the same person; ended, it is a pending sign-in that is over, which the sign-in page then names
// (useEndPending). It is one of the page's requests (attempt.ts); one that fails says the visitor is still signed in.
export function SignOutNote({ label, attempt, ended }: { label: string; attempt: Attempt; ended?: boolean }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const signOut = useSignOut();
  const endPending = useEndPending();
  const failed = attempt.failure?.kind === "signOut" ? attempt.failure.error : null;
  return (
    <>
      <NoteAction
        label={label}
        onPress={() => attempt.run("signOut", ended ? endPending : () => signOut.mutateAsync())}
      />
      <FormProblem message={failed ? t("signOutFailed", { reason: errors.message(failed) }) : ""} />
    </>
  );
}

// An address can be one unbroken run of 64 letters: a sentence that shows one may break it anywhere, or it would widen
// a phone's form. Set on the board's own element through its style.
export const wrapAnywhere = "overflow-wrap: anywhere";

// The primary button as the board draws a disabled one: the board's own rule for it loses to the primary button's fill,
// so a button that waits (for a box to be ticked, for the server) would look ready to press.
export const primaryLook = (disabled: boolean) =>
  disabled
    ? "cursor: default; color: var(--text2); background: var(--field); box-shadow: inset 0 0 0 var(--bw) var(--lineStrong)"
    : "";
