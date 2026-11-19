import { ApiError } from "@axiom/api";
import { useErrors } from "@axiom/app";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { type ReactNode, type RefObject, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Attempt } from "./attempt";
import type { Stage } from "./login-frame";
import { FormProblem, fieldRefused, hearAddress, InputField, primaryLook } from "./login-parts";

// What a sign-in page's request leaves to its form: how long a limit holds it, which field a refusal is about, and
// whether the request may have gone through.

// The language a mail is written in: the page's.
export function useMailLanguage(): "en" | "zh" {
  const { i18n } = useTranslation();
  return (i18n.resolvedLanguage ?? i18n.language).startsWith("en") ? "en" : "zh";
}

// useCooldown is the wait a limit's 429 asks for (Retry-After): sending sooner is refused the same way, so the step's
// submit waits until then, and then the refusal is over and its words go with it. The wait is the limit's own, kept
// here: another request on the page (a sign-out) clears the attempt's failure but not the server's limit. shown is the
// failure the step still says: the latest one, or while the wait lasts, the limit's.
export function useCooldown(failed: unknown) {
  const fresh = failed instanceof ApiError && failed.status === 429 ? failed : null;
  const [limit, setLimit] = useState<ApiError | null>(null);
  const [over, setOver] = useState<ApiError | null>(null);
  if (fresh && fresh !== limit) setLimit(fresh);
  useEffect(() => {
    if (!limit) return;
    const timer = window.setTimeout(() => setOver(limit), (limit.retryAfter ?? 60) * 1000);
    return () => window.clearTimeout(timer);
  }, [limit]);
  const waiting = limit !== null && over !== limit;
  const shown = fresh ? (waiting ? fresh : null) : (failed ?? (waiting ? limit : null));
  return { waiting, shown };
}

// useFocusAgain is the way back to a form whose step had left it: the step goes (leave), and once the form is on
// screen again its field takes the focus, so the address kept there can be edited at once.
export function useFocusAgain(input: RefObject<HTMLInputElement | null>) {
  const [back, setBack] = useState(0);
  useEffect(() => {
    if (back > 0) input.current?.focus();
  }, [input, back]);
  return (leave: () => void) => {
    leave();
    setBack((n) => n + 1);
  };
}

// An address the server does not take: the contract's guard names the field (400), and the server's own check answers
// 422 unprocessable (cmd/axiom/console_errors.go, ErrBadEmail).
export const badEmail = (error: unknown) =>
  fieldRefused(error, "/email") ||
  (error instanceof ApiError && error.status === 422 && error.code === "unprocessable");

// A password outside 12 to 256 characters: the contract's guard names the field (400, at pointer), the server's own
// check, which counts the same, answers weak_password.
export const badLength = (error: unknown, pointer: "/password" | "/new_password") =>
  fieldRefused(error, pointer) || (error instanceof ApiError && error.code === "weak_password");

// unanswered says a request that changes something may have gone through: no answer came, or any 5xx (the server's
// own 500 can follow a commit whose reply was lost, and its 503 for a request out of time says to look before trying
// again). The page says it may have, and offers to look, never a form error to send again.
export const unanswered = (error: unknown) => !(error instanceof ApiError) || error.status === 0 || error.status >= 500;

// useEmailStep is an email step's address (a reset's request, open registration): the field and what it holds, what is
// wrong with it (none typed, refusedBy: the server's refusal of it, cleared when it is edited) or with the request (the
// form's to say; a limit's wait stays), and the address to send.
export function useEmailStep(stage: Stage, attempt: Attempt, kind: string, refusedBy: (error: unknown) => boolean) {
  const { t } = useTranslation();
  const errors = useErrors();
  const input = useRef<HTMLInputElement | null>(null);
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const failed = attempt.failure?.kind === kind ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const refused = refusedBy(failed);
  const failure = badEmail(failed) ? t("auth.signIn.badEmail") : cooling.shown ? errors.message(cooling.shown) : "";
  return {
    input,
    email,
    blocked: attempt.kind !== null || cooling.waiting,
    problem: missing ? t("auth.signIn.missingEmail") : failure,
    invalid: missing || refused,
    type: (value: string) => {
      setEmail(value);
      setMissing(false);
      if (refused) attempt.dismiss();
      hearAddress(stage, value);
    },
    // the address the field holds when the step is sent, which a password manager may have written without a word to
    // the page; empty, which the step then says
    take: () => {
      const typed = input.current?.value ?? email;
      setEmail(typed);
      setMissing(!typed.trim());
      return typed.trim();
    },
  };
}

// usePasswordStep is a step that sets the password a mailed link asks for (a reset, a verification): the field, what is
// wrong with it (none typed, outside 12 to 256 characters: cleared when it is edited) or with the request (the form's to
// say; a limit's wait stays), and the password to send, which stays in its field until then. A failure is said once:
// the field's line, when it has one, stands for the step's.
export function usePasswordStep(
  attempt: Attempt,
  kind: string,
  pointer: "/password" | "/new_password",
  length: string,
) {
  const { t } = useTranslation();
  const errors = useErrors();
  const input = useRef<HTMLInputElement | null>(null);
  const [missing, setMissing] = useState(false);
  const failed = attempt.failure?.kind === kind ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const outside = badLength(failed, pointer);
  const said = missing ? t("auth.signIn.missingPassword") : outside ? length : "";
  return {
    blocked: attempt.kind !== null || cooling.waiting,
    field: {
      input,
      problem: said,
      invalid: missing || outside,
      onInput: () => {
        setMissing(false);
        if (outside) attempt.dismiss();
      },
    },
    problem: cooling.shown && !said ? errors.message(cooling.shown) : "",
    take: () => {
      const secret = input.current?.value ?? "";
      setMissing(!secret);
      return secret;
    },
  };
}

// PasswordStepBody is a password step's form (usePasswordStep): the field with its own line, what else the step asks
// (children: the documents to accept), the form's line, and the submit.
export function PasswordStepBody({
  id,
  label,
  step,
  submit,
  children,
}: {
  id: string;
  label: string;
  step: ReturnType<typeof usePasswordStep>;
  submit: string;
  children?: ReactNode;
}) {
  return (
    <>
      <InputField
        id={id}
        problemId={`${id}-problem`}
        label={label}
        name="password"
        type="password"
        autoComplete="new-password"
        {...step.field}
      />
      {children}
      <FormProblem id={`${id}-form-problem`} message={step.problem} />
      <LoginPrimary
        t={String}
        label={<span>{submit}</span>}
        values={{ disabled: step.blocked, style: primaryLook(step.blocked) }}
      />
    </>
  );
}
