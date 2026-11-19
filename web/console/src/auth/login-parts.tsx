import { useErrors, useSignOut } from "@axiom/app";
import LoginAction from "@axiom/boards/Login/regions/LoginAction";
import LoginError from "@axiom/boards/Login/regions/LoginError";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import type { KeyboardEvent, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { pressKey } from "../board";
import type { Attempt } from "./attempt";

// The parts the sign-in pages' steps share, made of the Login board's parts.

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
// someone else or as the same person. It is one of the page's requests (attempt.ts); one that fails says the visitor is
// still signed in.
export function SignOutNote({ label, attempt }: { label: string; attempt: Attempt }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const signOut = useSignOut();
  const failed = attempt.failure?.kind === "signOut" ? attempt.failure.error : null;
  return (
    <>
      <NoteAction label={label} onPress={() => attempt.run("signOut", () => signOut.mutateAsync())} />
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
