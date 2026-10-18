import { accountKey, readForm, useErrors } from "@axiom/app";
import { Button, Dialog, FormError, Icon, SubmitButton, TextAreaField, TextField } from "@axiom/ui";
import { useIsFetching, useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Attempt, failed, opened, type Sendable } from "./attempt";

// A change is one write of the admin API, which the person does for a reason that goes to the audit log, and which the
// server does at most once per Idempotency-Key (api/admin.yaml). ChangeDialog is the one way a page makes it: the
// fields of the change, the reason, a button that says what it does (and, for a risky change, a name to type first),
// and afterwards what happened, including when the answer is the stored result of an earlier try. When it cannot be
// known whether a try was done, the form locks and only that request is sent again; when the thing changed since it was
// read, nothing is sent until the form is filled again from what is current.

// What a form says when it cannot make the request from what was typed.
export type Problem = { problem: string };
export const isProblem = (value: unknown): value is Problem =>
  typeof value === "object" && value !== null && "problem" in value;

type Sent<T> = { data: T; replayed: boolean };
type Context = { reason: string; key: string };

type Props<Input, Out> = {
  title: string;
  intro?: ReactNode;
  action: string;
  danger?: boolean;
  // A name the person must type before a risky change is sent.
  confirm?: string;
  // The fields of the change, besides the reason; error is the failure of the last try, to mark a field.
  fields?: (error: unknown) => ReactNode;
  // The request's content from what was typed, or the problem with it.
  read: (values: Record<string, string>) => Input | Problem;
  send: (input: Input, context: Context) => Promise<Sent<Out>>;
  done: (out: Out) => ReactNode;
  onClose: () => void;
  // For a change made as of a revision: fills the dialog again from what the page has now (useSnapshot), once the
  // server has said the revision it read is stale.
  reload?: () => void;
};

type State = Attempt<unknown>["state"];

// A request as it first went out: its key and body are fixed in it, so sending it again after a lost answer sends exactly
// that, whatever the page has refreshed since.
type Call<Out> = () => Promise<Sent<Out>>;

const newKey = () => crypto.randomUUID();

// One key per intended change, made when the dialog opens: pressing the button again, or sending after a lost
// response, is the same change and not a second one (attempt.ts). A change that was done refreshes everything the admin
// shows.
function useChange<Out>() {
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: [accountKey] });
  const [attempt, setAttempt] = useState<Attempt<Call<Out>>>(() => opened(newKey()));
  const mutation = useMutation({
    // The variables hold what the person typed, a supplier's login among it. Nothing keeps them once the dialog is
    // gone: gcTime 0 drops the cache entry as soon as no dialog watches it.
    gcTime: 0,
    mutationFn: ({ call }: { from: Sendable<Call<Out>>; call: Call<Out> }) => call(),
    onError: (error, { from, call }) => {
      const next = failed(from, call, error, newKey);
      setAttempt(next);
      // Someone changed the thing since it was read: fetch what it is now, for the form to be filled from.
      if (next.state === "stale") {
        void refresh();
      }
    },
    onSuccess: refresh,
  });
  return {
    attempt,
    mutation,
    refresh,
    send: (from: Sendable<Call<Out>>, call: Call<Out>) => mutation.mutate({ from, call }),
    restart: () => {
      mutation.reset();
      setAttempt(opened(newKey()));
    },
  };
}

type Change<Out> = ReturnType<typeof useChange<Out>>;

// The thing a dialog changes, as the person read it. The form is filled from this copy and the revision the change is
// made against is this copy's, so a refresh of the page underneath cannot pair a new revision with the old content and
// put that over someone else's change. reload takes what the page has now.
export function useSnapshot<T>(live: T) {
  const [read, setRead] = useState(live);
  return [read, () => setRead(live)] as const;
}

export function ChangeDialog<Input, Out>(props: Props<Input, Out>) {
  const { done, onClose, title, reload } = props;
  const change = useChange<Out>();
  // The form is made again when it is filled from what is current, so its fields take their new initial values.
  const [form, setForm] = useState(0);
  const { state } = change.attempt;
  // Whether an earlier try was done is not known here: the page underneath is fetched again so it does not keep showing
  // things from before the try, though what comes back cannot prove the try was not done (it may still be committing).
  const uncertain = !change.mutation.isSuccess && (state === "unknown" || state === "reused");
  const close = () => {
    if (uncertain) {
      void change.refresh();
    }
    onClose();
  };
  const refill =
    reload &&
    (() => {
      reload();
      change.restart();
      setForm((made) => made + 1);
    });
  return (
    <Dialog title={title} onClose={close}>
      {change.mutation.isSuccess ? (
        <Result replayed={change.mutation.data.replayed} onClose={close}>
          {done(change.mutation.data.data)}
        </Result>
      ) : (
        <ChangeForm key={form} {...props} onClose={close} change={change} reload={refill} />
      )}
    </Dialog>
  );
}

function ChangeForm<Input, Out>({
  intro,
  action,
  danger,
  confirm,
  fields,
  read,
  send,
  onClose,
  reload,
  change,
}: Props<Input, Out> & { change: Change<Out> }) {
  const { t } = useTranslation();
  const [problem, setProblem] = useState<string | null>(null);
  const { attempt, mutation } = change;
  const refusal = (values: Record<string, string>, reason: string) => {
    if (!reason) {
      return t("change.reasonMissing");
    }
    return confirm !== undefined && values.confirm !== confirm ? t("change.confirmMismatch") : null;
  };
  const submit = (form: HTMLFormElement) => {
    if (mutation.isPending || attempt.state === "stale" || attempt.state === "reused") {
      return;
    }
    if (attempt.state === "unknown") {
      change.send(attempt, attempt.request);
      return;
    }
    const values = readForm(form);
    const reason = (values.reason ?? "").trim();
    const input = read(values);
    const found = isProblem(input) ? input.problem : refusal(values, reason);
    setProblem(found);
    if (found === null) {
      const { key } = attempt;
      change.send(attempt, () => send(input as Input, { reason, key }));
    }
  };
  const editing = attempt.state === "editing";
  const said = <Said problem={problem} state={attempt.state} error={mutation.error} />;
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        submit(event.currentTarget);
      }}
    >
      {intro ? <div className="space-y-2 text-t2">{intro}</div> : null}
      {editing ? said : null}
      {/* Only a form being edited can change, and not while what it holds is on its way: the request already fixed is
          what a lost answer resends, so the form must keep showing it. */}
      <fieldset disabled={!editing || mutation.isPending} className="min-w-0 space-y-4">
        <ChangeFields fields={fields} confirm={confirm} error={mutation.error} />
      </fieldset>
      {/* Why the button now does something else sits next to it, where the person is looking after pressing it. */}
      {editing ? null : said}
      <Footer
        state={attempt.state}
        action={action}
        danger={danger}
        pending={mutation.isPending}
        reload={reload}
        onClose={onClose}
      />
    </form>
  );
}

function ChangeFields({
  fields,
  confirm,
  error,
}: Pick<Props<unknown, unknown>, "fields" | "confirm"> & { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      {fields?.(error)}
      <TextAreaField
        label={t("change.reason")}
        hint={t("change.reasonHint")}
        name="reason"
        rows={2}
        maxLength={1000}
        className="font-sans"
        error={errors.field(error, "reason")}
      />
      {confirm === undefined ? null : (
        <TextField
          label={t("change.confirmLabel", { name: confirm })}
          name="confirm"
          autoComplete="off"
          spellCheck={false}
        />
      )}
    </>
  );
}

// Said tells what stopped the change: a problem with what was typed, the server's refusal, or, when the dialog cannot
// simply send again, why not and what happens next.
function Said({ problem, state, error }: { problem: string | null; state: State; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (state === "stale" || state === "reused") {
    return <FormError>{t(`change.${state}`)}</FormError>;
  }
  const said = problem ?? (error ? errors.message(error) : null);
  if (state === "unknown") {
    return (
      <FormError>
        {said ? <p>{said}</p> : null}
        <p className="text-t2">{t("change.unknown")}</p>
      </FormError>
    );
  }
  return said ? <FormError>{said}</FormError> : null;
}

type Moves = {
  state: State;
  action: string;
  danger?: boolean;
  pending: boolean;
  reload?: () => void;
  onClose: () => void;
};

// Footer holds the buttons. In a form taller than the screen, the message shown above them after a press would land
// below what the person sees, so they are brought into view.
function Footer(props: Moves) {
  const { t } = useTranslation();
  const ref = useRef<HTMLDivElement>(null);
  const editing = props.state === "editing";
  useEffect(() => {
    if (!editing) {
      ref.current?.scrollIntoView({ block: "nearest" });
    }
  }, [editing]);
  return (
    <div ref={ref} className="flex justify-end gap-2 pt-2">
      {props.state === "reused" ? null : (
        <Button size="md" onClick={props.onClose}>
          {t("cancel", { ns: "common" })}
        </Button>
      )}
      <Next {...props} />
    </div>
  );
}

// Next is the button that moves the change on: send it, send the same request again, fill the form from what is
// current (once that has arrived), or close.
function Next({ state, action, danger, pending, reload, onClose }: Moves) {
  const { t } = useTranslation();
  const fetching = useIsFetching({ queryKey: [accountKey] }) > 0;
  if (state === "stale" && reload) {
    return (
      <Button variant="primary" size="md" disabled={fetching} aria-busy={fetching} onClick={reload}>
        {fetching ? <Icon name="refresh" className="animate-spin" /> : null}
        {t("change.reload")}
      </Button>
    );
  }
  if (state === "stale" || state === "reused") {
    return (
      <Button variant="primary" size="md" onClick={onClose}>
        {t("change.close")}
      </Button>
    );
  }
  return (
    <SubmitButton pending={pending} variant={danger ? "danger" : "primary"}>
      {state === "unknown" ? t("change.resend") : action}
    </SubmitButton>
  );
}

// Result says what the change did. An answer that is the stored result of an earlier try is said to be one: nothing was
// done a second time.
function Result({ replayed, onClose, children }: { replayed: boolean; onClose: () => void; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-4">
      <p role="status" className="flex items-start gap-2 text-t1">
        <Icon name="check" className="mt-0.5 shrink-0 text-ok" />
        <span>{t("change.done")}</span>
      </p>
      {replayed ? (
        <p role="status" className="rounded-btn bg-s2 px-3 py-2 text-t2">
          {t("change.replayed")}
        </p>
      ) : null}
      <div className="space-y-2 text-t2">{children}</div>
      <div className="flex justify-end">
        <Button variant="primary" size="md" onClick={onClose}>
          {t("change.close")}
        </Button>
      </div>
    </div>
  );
}
