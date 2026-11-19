import { ApiError, consoleApi, registrationOf, type Schemas, unwrap } from "@axiom/api";
import { consoleAuth } from "@axiom/api/auth";
import { useErrors, useReadSession, useStartSession } from "@axiom/app";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { type Attempt, useAttempt } from "./attempt";
import { canSubmitWithLegal, LegalConsent, type LegalConsentState, useLegalConsent } from "./legal";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import {
  EmailField,
  FormProblem,
  fieldRefused,
  hearAddress,
  InputField,
  LoginGate,
  NoteAction,
  primaryLook,
  RouteNote,
} from "./login-parts";
import { solveProof } from "./proof";
import {
  badEmail,
  badLength,
  unanswered,
  useCooldown,
  useEmailStep,
  useFocusAgain,
  useMailLanguage,
} from "./request-state";
import { AnswerStep } from "./way-on";

// Registration, as the site takes new users (getSite's registration): with an invite code, which makes the account and
// then signs in with the same email and password; open, which mails a link whose page (verify-email.tsx) sets the
// password; or not at all. A browser signed in already is sent on by the gate, as on the sign-in page.
export default function Register() {
  return (
    <LoginGate stage="guest">
      <RegisterPage />
    </LoginGate>
  );
}

function RegisterPage() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const stage = useStage();
  const attempt = useAttempt();
  if (!site.data) return <SiteUnread site={site} stage={stage} attempt={attempt} />;
  const registration = registrationOf(site.data);
  if (registration === "open") return <OpenRegistration site={site.data} stage={stage} attempt={attempt} />;
  if (registration === "invite") return <InviteRegistration stage={stage} attempt={attempt} />;
  return (
    <RegisterFrame sub={t("auth.register.closed")} stage={stage} attempt={attempt} step="closed">
      <HaveAccount />
    </RegisterFrame>
  );
}

// The form of a step that asks: its title, its line, and what to do with a submit (none: a step with nothing to send).
function RegisterFrame({
  sub,
  step,
  stage,
  attempt,
  onSubmit,
  children,
}: {
  sub?: string;
  step: string;
  stage: Stage;
  attempt: Attempt;
  onSubmit?: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <LoginFrame
      title={t("auth.register.title")}
      sub={sub}
      step={step}
      shape="start"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit?.();
      }}
    >
      {children}
    </LoginFrame>
  );
}

// The way to sign in from a registration that has not happened: the gate keeps this page to a browser signed out.
function HaveAccount() {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  return (
    <RouteNote to="/sign-in" lead={t("auth.register.haveAccount")} label={t("auth.register.signIn")} next={next} />
  );
}

// Before the site's settings say how it takes new users: their read under way, or failed, with a retry.
function SiteUnread({
  site,
  stage,
  attempt,
}: {
  site: UseQueryResult<Schemas["Site"]>;
  stage: Stage;
  attempt: Attempt;
}) {
  const { t } = useTranslation();
  return (
    <RegisterFrame step="site" stage={stage} attempt={attempt}>
      {site.isError ? (
        <>
          <FormProblem message={t("auth.register.siteFailed")} />
          <NoteAction label={t("auth.register.retry")} onPress={() => void site.refetch()} />
        </>
      ) : (
        <LoginNote t={String}>
          <span role="status">{t("auth.register.loading")}</span>
        </LoginNote>
      )}
      <HaveAccount />
    </RegisterFrame>
  );
}

// What a submitted registration led to, when the page does not go on by itself (a sign-in moves the gate): the mail is
// on its way, the account was made but signing in did not work, or the answer never came.
type Outcome = "sent" | "made" | "unknown";

// An address the server does not take for a registration: not an address, a domain the site does not take, or one
// registered already.
const emailRefused = (error: unknown) =>
  badEmail(error) ||
  (error instanceof ApiError && (error.code === "email_domain_not_allowed" || error.code === "email_taken"));

// The registration with an invite code. Not cancellable once sent: the account it makes is made, and the sign-in after
// it sets the session cookie. Once the account is made (201) the page never registers again: a session the sign-in
// started moves the gate on; none, or none that could be read, leaves the page saying the account is made, whose way on
// reads the session again (a cookie a failed sign-in may have set included).
function InviteRegistration({ stage, attempt }: { stage: Stage; attempt: Attempt }) {
  const { t } = useTranslation();
  const legal = useLegalConsent();
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const form = useInviteForm(stage, attempt, legal, setOutcome);
  const editAgain = useFocusAgain(form.email.input);
  if (outcome)
    return (
      <Registered
        outcome={outcome}
        open={false}
        attempt={attempt}
        stage={stage}
        onAgain={() =>
          editAgain(() => {
            attempt.dismiss();
            setOutcome(null);
          })
        }
      />
    );
  return (
    <RegisterFrame
      sub={t("auth.register.inviteDescription")}
      step="invite"
      stage={stage}
      attempt={attempt}
      onSubmit={form.submit}
    >
      <InviteFields form={form} />
      <LegalConsent consent={legal} />
      <FormProblem id="register-problem" message={form.problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.register.submit")}</span>}
        values={{ disabled: form.blocked, style: primaryLook(form.blocked) }}
      />
      <HaveAccount />
    </RegisterFrame>
  );
}

// The invite form's fields, each with its own line for what is wrong with it.
function InviteFields({ form }: { form: ReturnType<typeof useInviteForm> }) {
  const { t } = useTranslation();
  return (
    <>
      <InputField
        id="register-invite"
        problemId="register-invite-problem"
        label={t("auth.register.invite")}
        name="invite_code"
        type="text"
        autoComplete="off"
        value={form.invite.start}
        input={form.invite.input}
        problem={form.invite.problem}
        invalid={form.invite.problem !== ""}
        onInput={form.invite.type}
      />
      <EmailField
        id="register-email"
        problemId="register-email-problem"
        email={form.email.value}
        input={form.email.input}
        problem={form.email.problem}
        invalid={form.email.problem !== ""}
        onInput={form.email.type}
      />
      <InputField
        id="register-password"
        problemId="register-password-problem"
        label={t("auth.password", { ns: "common" })}
        name="password"
        type="password"
        autoComplete="new-password"
        input={form.password.input}
        problem={form.password.problem}
        invalid={form.password.problem !== ""}
        onInput={form.password.type}
      />
    </>
  );
}

// useInviteForm holds the invite form: its fields (useInviteFields) and the registration, then the sign-in with the same
// email and password.
function useInviteForm(
  stage: Stage,
  attempt: Attempt,
  legal: LegalConsentState,
  onOutcome: (outcome: Outcome) => void,
) {
  const errors = useErrors();
  const start = useStartSession();
  const read = useReadSession();
  const failed = attempt.failure?.kind === "register" ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const fields = useInviteFields(stage, failed);
  const blocked = attempt.kind !== null || cooling.waiting || !canSubmitWithLegal(legal);
  const register = async (body: Schemas["RegisterRequest"]) => {
    const result = await unwrap(consoleApi.POST("/api/console/v1/auth/register", { body }));
    if (!("user_id" in result)) return onOutcome("sent");
    const session = await consoleAuth
      .signIn({ email: body.email, password: body.password ?? "" })
      .then((grant) => start(grant.csrf_token), read)
      .catch(() => null);
    if (!session) onOutcome("made");
  };
  return {
    ...fields,
    problem: cooling.shown && !fields.refused && !fields.said ? errors.message(cooling.shown) : "",
    blocked,
    submit: () => {
      const body = fields.read();
      if (!body || blocked) return;
      attempt.run("register", () => register({ ...body, consent: legal.versions }), {
        onFail: (error) => {
          if (unanswered(error)) onOutcome("unknown");
          else stage.hear({ type: "bad" });
          if (error instanceof ApiError && error.code === "consent_required") legal.reload();
        },
      });
    },
  };
}

// useInviteFields holds the invite form's three fields: what each says (none typed, or the refusal about it, in the
// page's words where they say more), cleared when that field is edited and only for it (edited: since the failure they
// were refused by), and what they hold when the form is sent; the address read then is kept for the form's way back, as
// a password manager may have written it without a word to the page.
function useInviteFields(stage: Stage, failed: unknown) {
  const { t } = useTranslation();
  const errors = useErrors();
  const refs = {
    invite: useRef<HTMLInputElement | null>(null),
    email: useRef<HTMLInputElement | null>(null),
    password: useRef<HTMLInputElement | null>(null),
  };
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState({ invite: false, email: false, password: false });
  const [edited, setEdited] = useState<{ of: unknown; names: string[] }>({ of: null, names: [] });
  const done = edited.of === failed ? edited.names : [];
  const refusedAt = {
    invite: fieldRefused(failed, "/invite_code") || (failed instanceof ApiError && failed.code === "invalid_invite"),
    email: emailRefused(failed),
    password: badLength(failed, "/password"),
  };
  const refusal = {
    invite: errors.message(failed),
    email: badEmail(failed) ? t("auth.signIn.badEmail") : errors.message(failed),
    password: t("auth.register.length"),
  };
  const missingText = {
    invite: t("auth.register.missingInvite"),
    email: t("auth.signIn.missingEmail"),
    password: t("auth.signIn.missingPassword"),
  };
  const about = (name: keyof typeof refusedAt) => refusedAt[name] && !done.includes(name);
  const said = (name: keyof typeof refusedAt) => (missing[name] ? missingText[name] : about(name) ? refusal[name] : "");
  const field = (name: keyof typeof refusedAt) => ({
    input: refs[name],
    problem: said(name),
    type: () => {
      setMissing((now) => ({ ...now, [name]: false }));
      if (about(name)) setEdited({ of: failed, names: [...done, name] });
    },
  });
  const typeEmail = field("email").type;
  return {
    invite: { ...field("invite"), start: useSearch({ strict: false }).invite ?? "" },
    email: {
      ...field("email"),
      value: email,
      type: (value: string) => {
        setEmail(value);
        typeEmail();
        hearAddress(stage, value);
      },
    },
    password: field("password"),
    refused: refusedAt.invite || refusedAt.email || refusedAt.password,
    said: Boolean(said("invite") || said("email") || said("password")),
    // what the fields hold when the form is sent, or null when one is empty, which it then says
    read: () => {
      const invite = (refs.invite.current?.value ?? "").trim();
      setEmail(refs.email.current?.value ?? email);
      const address = (refs.email.current?.value ?? email).trim();
      const password = refs.password.current?.value ?? "";
      setMissing({ invite: !invite, email: !address, password: !password });
      return invite && address && password ? { invite_code: invite, email: address, password } : null;
    },
  };
}

// Open registration: the address, which the server mails a link to. The site's proof of work, where it asks for one,
// is solved in the page first, which holds the page while it runs.
function OpenRegistration({ site, stage, attempt }: { site: Schemas["Site"]; stage: Stage; attempt: Attempt }) {
  const { t } = useTranslation();
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const form = useOpenForm(site, stage, attempt, setOutcome);
  const editAgain = useFocusAgain(form.input);
  if (outcome)
    return (
      <Registered
        outcome={outcome}
        open
        attempt={attempt}
        stage={stage}
        onAgain={() =>
          editAgain(() => {
            attempt.dismiss();
            setOutcome(null);
          })
        }
      />
    );
  return (
    <RegisterFrame
      sub={t("auth.register.openDescription")}
      step="open"
      stage={stage}
      attempt={attempt}
      onSubmit={form.submit}
    >
      <EmailField
        id="register-email"
        problemId="register-email-problem"
        email={form.email}
        input={form.input}
        problem={form.problem}
        invalid={form.invalid}
        onInput={form.type}
      />
      <FormProblem id="register-form-problem" message={form.formProblem} />
      {form.working ? (
        <LoginNote t={String}>
          <span role="status">{t("auth.register.working")}</span>
        </LoginNote>
      ) : null}
      <LoginPrimary
        t={String}
        label={<span>{t("auth.register.submitOpen")}</span>}
        values={{ disabled: form.blocked, style: primaryLook(form.blocked) }}
      />
      <HaveAccount />
    </RegisterFrame>
  );
}

// useOpenForm is open registration's email step (useEmailStep) and its request, after the site's proof of work.
function useOpenForm(site: Schemas["Site"], stage: Stage, attempt: Attempt, onOutcome: (outcome: Outcome) => void) {
  const language = useMailLanguage();
  const step = useEmailStep(stage, attempt, "register", emailRefused);
  const [working, setWorking] = useState(false);
  const send = async (address: string) => {
    let human: string | undefined;
    if (site.human_check && site.id) {
      setWorking(true);
      // let the note paint before the solver takes the main thread
      await new Promise((resolve) => window.setTimeout(resolve, 0));
      human = await solveProof(site.id, address.toLowerCase(), site.human_check.bits).finally(() => setWorking(false));
    }
    await unwrap(
      consoleApi.POST("/api/console/v1/auth/register", { body: { email: address, human_check: human, language } }),
    );
    onOutcome("sent");
  };
  return {
    ...step,
    working,
    submit: () => {
      const address = step.take();
      if (!address || step.blocked) return;
      attempt.run("register", () => send(address), {
        onFail: (error) => (unanswered(error) ? onOutcome("unknown") : stage.hear({ type: "bad" })),
      });
    },
  };
}

// A registration's answer: the mail is on its way (or may be), the account was made, or the answer never came. The
// form is one step back, its address kept, to send again or register again.
function Registered({
  outcome,
  open,
  attempt,
  stage,
  onAgain,
}: {
  outcome: Outcome;
  open: boolean;
  attempt: Attempt;
  stage: Stage;
  onAgain: () => void;
}) {
  const { t } = useTranslation();
  const words = {
    sent: { title: t("auth.register.sentTitle"), sub: t("auth.register.sent"), again: t("auth.register.resend") },
    made: { title: t("auth.register.madeTitle"), sub: t("auth.register.made"), again: "" },
    unknown: {
      title: t("auth.register.unknownTitle"),
      sub: open ? t("auth.register.unknownOpen") : t("auth.register.unknown"),
      again: open ? t("auth.register.resend") : t("auth.register.again"),
    },
  }[outcome];
  return (
    <AnswerStep
      title={words.title}
      sub={words.sub}
      step={outcome}
      shape="enter"
      signIn={t("auth.register.signIn")}
      attempt={attempt}
      stage={stage}
    >
      {words.again ? <NoteAction label={words.again} onPress={onAgain} /> : null}
    </AnswerStep>
  );
}
