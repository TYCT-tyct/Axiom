import { ApiError } from "@axiom/api";
import { Gate, sessionBinding, useCachedSession, useEndPending, useErrors, useSignOut } from "@axiom/app";
import LoginAction from "@axiom/boards/Login/regions/LoginAction";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginError from "@axiom/boards/Login/regions/LoginError";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginLink from "@axiom/boards/Login/regions/LoginLink";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import { pressBoardButton } from "@axiom/ui";
import {
  type AnchorHTMLAttributes,
  type ComponentProps,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
  useLayoutEffect,
  useMemo,
} from "react";
import { useTranslation } from "react-i18next";
import type { Attempt } from "./attempt";
import type { Stage } from "./login-frame";
import { useLoginNavigation } from "./navigation";

// The parts the sign-in pages' steps share, made of the Login board's parts.

// LoginGate is a sign-in page's gate (@axiom/app Gate), with the page set in the sign-in pages' faces (useLoginFace):
// what shows before the board (the gate's loading and failure states) as well as the board.
export function LoginGate({ stage, children }: { stage: ComponentProps<typeof Gate>["stage"]; children: ReactNode }) {
  useLoginFace();
  return <Gate stage={stage}>{children}</Gate>;
}

// useLoginFace sets the page in the faces cut for the sign-in pages (web/scripts/fonts/sources.json: Axiom CJK). The
// console's own body face is cut for its other pages, and would load a second Chinese face here. The family is the
// root's --family-ui (console/src/main.css), changed while the page is shown, as the brand's colours are
// (useBrandStyle); a layout effect, so the first frame has it.
export function useLoginFace() {
  useLayoutEffect(() => {
    const root = document.documentElement;
    root.style.setProperty("--family-ui", "var(--family-site)");
    return () => {
      root.style.removeProperty("--family-ui");
    };
  }, []);
}

// Shown to the stage as an address that looks whole; the server decides what an address is.
const looksLikeEmail = (value: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);

// hearAddress tells the stage how whole the address typed looks.
export const hearAddress = (stage: Stage, value: string) =>
  stage.hear({ type: "email", len: value.trim().length, ok: looksLikeEmail(value.trim()) });

// fieldRefused says the server's answer names this part of the request as what is wrong (the contract's 400, with its
// pointer).
export const fieldRefused = (error: unknown, pointer: string) =>
  error instanceof ApiError && error.fields.some((field) => field.pointer === pointer);

// EmailField is an email step's address with the step's error line under it (problemId), which the field points at.
// invalid marks the field itself, only when what it holds is the problem (none typed, or refused): a limit or a server
// that is down is the form's to say, not the address's. input is the field itself, which a password manager may write
// without a word to the page.
export function EmailField({
  id,
  problemId,
  email,
  input,
  problem,
  invalid,
  onInput,
}: {
  id: string;
  problemId: string;
  email: string;
  input: RefObject<HTMLInputElement | null>;
  problem: string;
  invalid: boolean;
  onInput: (value: string) => void;
}) {
  const { t } = useTranslation("common");
  return (
    <>
      <LoginEmail
        t={String}
        text={{ label: t("auth.email") }}
        values={{
          id,
          name: "email",
          type: "email",
          autoComplete: "username",
          placeholder: "name@company.com",
          value: email,
          fieldCls: invalid ? "bad" : "",
          describedBy: problem ? problemId : "",
          invalid: invalid || undefined,
          input: (element) => {
            input.current = element as HTMLInputElement | null;
          },
          onInput: (event) => onInput((event.currentTarget as HTMLInputElement).value),
        }}
      />
      <FormProblem id={problemId} message={problem} />
    </>
  );
}

// InputField is a step's other field (a password, an invite code) with its error line under it (problemId), which the
// field points at; invalid marks the field, as for EmailField. A password stays in its field until it is sent, never in
// the page's state: value is only where the field starts (an invite code the link carries).
export function InputField({
  id,
  problemId,
  label,
  name,
  type,
  autoComplete,
  value,
  input,
  problem,
  invalid,
  onInput,
}: {
  id: string;
  problemId: string;
  label: string;
  name: string;
  type: "password" | "text";
  autoComplete: "current-password" | "new-password" | "off";
  value?: string;
  input: RefObject<HTMLInputElement | null>;
  problem: string;
  invalid: boolean;
  onInput: () => void;
}) {
  return (
    <>
      <LoginField
        t={String}
        text={{ label }}
        values={{
          id,
          name,
          type,
          autoComplete,
          value,
          fieldCls: invalid ? "bad" : "",
          describedBy: problem ? problemId : "",
          invalid: invalid || undefined,
          input: (element) => {
            input.current = element as HTMLInputElement | null;
          },
          onInput,
        }}
      />
      <FormProblem id={problemId} message={problem} />
    </>
  );
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
          onKeyDown: (event) => pressBoardButton(event as KeyboardEvent<Element>),
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
  // the cache's session, not the gate's: a page outside a gate (the reset's) signs out too
  const pending = sessionBinding(useCachedSession());
  const failed = attempt.failure?.kind === "signOut" ? attempt.failure.error : null;
  return (
    <>
      <NoteAction
        label={label}
        onPress={() => attempt.run("signOut", ended ? () => endPending(pending) : () => signOut.mutateAsync())}
      />
      <FormProblem message={failed ? t("signOutFailed", { reason: errors.message(failed) }) : ""} />
    </>
  );
}

// RouteNote is a note under a step that leads to another sign-in page, carrying where the visitor was going; lead is
// the note's words before its link.
export function RouteNote({ to, label, lead, next }: { to: RouteTo; label: string; lead?: string; next?: string }) {
  const { Link } = useLoginNavigation();
  const RouteLink = useMemo(
    () =>
      ({ href: _, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) => <Link to={to} search={{ next }} {...props} />,
    [Link, to, next],
  );
  return (
    <LoginNote t={String}>
      {lead ? (
        <>
          <span>{lead}</span>{" "}
        </>
      ) : null}
      <LoginLink t={String} link={RouteLink} label={label} values={{ to }} />
    </LoginNote>
  );
}

// The sign-in pages a note leads to, which all take where the visitor was going (router.tsx guestSearch).
type RouteTo = "/sign-in" | "/register" | "/reset-password" | "/mfa";

// An address can be one unbroken run of 64 letters: a sentence that shows one may break it anywhere, or it would widen
// a phone's form. Set on the board's own element through its style.
export const wrapAnywhere = "overflow-wrap: anywhere";

// The primary button as the board draws a disabled one: the board's own rule for it loses to the primary button's fill,
// so a button that waits (for a box to be ticked, for the server) would look ready to press.
export const primaryLook = (disabled: boolean) =>
  disabled
    ? "cursor: default; color: var(--text2); background: var(--field); box-shadow: inset 0 0 0 var(--bw) var(--lineStrong)"
    : "";
