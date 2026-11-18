import { type Attempt, accountKey, failed, opened, readForm, type Sendable, useErrors, useSession } from "@axiom/app";
import { Dialog, TextAreaField, TextField } from "@axiom/boards/ds";
import { FormError, Stack, Tag, Text } from "@axiom/ui";
import { useIsFetching, useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { unsettled } from "./unsettled";

// A change is one write of the admin API, which the person does for a reason that goes to the audit log, and which the
// server does at most once per Idempotency-Key (api/admin.yaml). ChangeDialog is the one way a page makes it: the
// fields of the change, the reason, a button that says what it does (and, for a risky change, a name to type first),
// and afterwards what happened, including when the answer is the stored result of an earlier try. When it cannot be
// known whether a try was done, the form locks and only that request is sent again, also after the dialog was closed
// and opened again or the person signed in again (unsettled.ts); when the thing changed since it was read, nothing is
// sent until the form is filled again from what is current.

// What a form says when it cannot make the request from what was typed.
export type Problem = { problem: string };
export const isProblem = (value: unknown): value is Problem =>
  typeof value === "object" && value !== null && "problem" in value;

type Sent<T> = { data: T; replayed: boolean };
type Context = { reason: string; key: string };

type Props<Input, Out> = {
  title: string;
  // What is being changed, the same each time its dialog opens, so an unsettled try is met again: the kind of change and
  // the thing it is made to ("credit-grant:" + the account's id).
  operation: string;
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
  // A change whose result says for itself how it went (a test that ran and failed), shown without the generic "Done".
  verdict?: boolean;
  // What the person reads while a slow change is on its way, beside the locked form.
  waiting?: ReactNode;
  // What a refusal means for this change, where the error code's words for every page would not say; undefined keeps
  // those.
  explain?: (error: unknown) => string | undefined;
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
// response, is the same change and not a second one (attempt.ts). A try that is unsettled is held outside the dialog
// and found again when it opens, as the request that must be settled before anything else is sent. A change that was
// done refreshes everything the admin shows.
function useChange<Out>(operation: string) {
  const queryClient = useQueryClient();
  const { user, account } = useSession();
  const scope = `${user.id}/${account}/${operation}`;
  const refresh = () => queryClient.invalidateQueries({ queryKey: [accountKey] });
  const [carried] = useState(() => unsettled.get(scope));
  const [attempt, setAttempt] = useState<Attempt<Call<Out>>>(() =>
    carried ? { state: "unknown", key: carried.key, request: carried.request as Call<Out> } : opened(newKey()),
  );
  // Whether the form this try was typed in is gone: the dialog was opened again, and what is left is the request.
  const [lost, setLost] = useState(carried !== undefined);
  const mutation = useMutation({
    // The variables hold what the person typed, a supplier's login among it. Nothing keeps them once the dialog is
    // gone: gcTime 0 drops the cache entry as soon as no dialog watches it.
    gcTime: 0,
    mutationFn: ({ call }: { from: Sendable<Call<Out>>; call: Call<Out> }) => call(),
    onError: (error, { from, call }) => {
      const next = failed(from, call, error, newKey);
      setAttempt(next);
      if (next.state === "unknown") {
        unsettled.hold(scope, { key: next.key, request: next.request });
      } else {
        unsettled.drop(scope, from.key);
      }
      // Someone changed the thing since it was read: fetch what it is now, for the form to be filled from.
      if (next.state === "stale") {
        void refresh();
      }
    },
    onSuccess: (_, { from }) => {
      unsettled.drop(scope, from.key);
      return refresh();
    },
  });
  const restart = () => {
    unsettled.drop(scope);
    mutation.reset();
    setLost(false);
    setAttempt(opened(newKey()));
  };
  return {
    attempt,
    mutation,
    refresh,
    lost,
    send: (from: Sendable<Call<Out>>, call: Call<Out>) => {
      unsettled.own(scope, from.key);
      mutation.mutate({ from, call });
    },
    restart,
  };
}

// The thing a dialog changes, as the person read it. The form is filled from this copy and the revision the change is
// made against is this copy's, so a refresh of the page underneath cannot pair a new revision with the old content and
// put that over someone else's change. reload takes what the page has now.
export function useSnapshot<T>(live: T) {
  const [read, setRead] = useState(live);
  return [read, () => setRead(live)] as const;
}

type FieldProblem = { message: string; field?: "reason" | "confirm" };
type Change<Out> = ReturnType<typeof useChange<Out>>;

export function ChangeDialog<Input, Out>(props: Props<Input, Out>) {
  const { done, onClose, title, reload, verdict, action, danger } = props;
  const { t } = useTranslation();
  const change = useChange<Out>(props.operation);
  const { attempt, mutation, lost } = change;
  const { state } = attempt;
  const form = useChangeForm(props, change);
  const fetching = useIsFetching({ queryKey: [accountKey] }) > 0;
  const close = () => {
    if (!mutation.isSuccess && (state === "unknown" || state === "reused")) void change.refresh();
    onClose();
  };
  const moves = {
    state,
    lost,
    success: mutation.isSuccess,
    fetching,
    pending: mutation.isPending,
    formId: form.id,
    action,
    danger,
    reload: reload ? form.refill : undefined,
    restart: change.restart,
    close,
  };
  const labels = {
    close: t("change.close"),
    reload: t("change.reload"),
    resend: t("change.resend"),
    giveUp: t("change.giveUp"),
    cancel: t("cancel", { ns: "common" }),
  };
  return (
    <Dialog
      title={title}
      closeLabel={t("dialog.close", { ns: "common" })}
      onClose={close}
      primary={next(moves, labels)}
      secondary={back(moves, labels)}
    >
      {mutation.isSuccess ? (
        <Result replayed={mutation.data.replayed} verdict={verdict}>
          {done(mutation.data.data)}
        </Result>
      ) : (
        <form
          key={form.version}
          id={form.id}
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            form.submit(event.currentTarget);
          }}
        >
          <Stack>
            <ChangeBody {...props} change={change} problem={form.problem} />
          </Stack>
        </form>
      )}
    </Dialog>
  );
}

function useChangeForm<Input, Out>({ read, send, confirm, reload }: Props<Input, Out>, change: Change<Out>) {
  const { t } = useTranslation();
  const { attempt, mutation } = change;
  const id = useId();
  // Refill remounts the form so its fields take their new initial values.
  const [version, setVersion] = useState(0);
  const [problem, setProblem] = useState<FieldProblem>();
  const refill = () => {
    reload?.();
    change.restart();
    setProblem(undefined);
    setVersion((made) => made + 1);
  };
  const submit = (element: HTMLFormElement) => {
    if (mutation.isPending) return;
    if (attempt.state === "unknown") {
      change.send(attempt, attempt.request);
      return;
    }
    if (attempt.state !== "editing") return;
    const values = readForm(element);
    const reason = (values.reason ?? "").trim();
    const input = read(values);
    if (isProblem(input)) {
      setProblem({ message: input.problem });
    } else if (!reason) {
      setProblem({ message: t("change.reasonMissing"), field: "reason" });
    } else if (confirm !== undefined && values.confirm !== confirm) {
      setProblem({ message: t("change.confirmMismatch"), field: "confirm" });
    } else {
      setProblem(undefined);
      const { key } = attempt;
      change.send(attempt, () => send(input, { reason, key }));
    }
  };
  return { id, version, problem, refill, submit };
}

type Moves = {
  state: State;
  lost: boolean;
  success: boolean;
  fetching: boolean;
  pending: boolean;
  formId: string;
  action: string;
  danger?: boolean;
  reload?: () => void;
  restart: () => void;
  close: () => void;
};

function next(moves: Moves, labels: { reload: string; close: string; resend: string }) {
  const { state, lost, success, fetching, pending, formId, action, danger, reload, close } = moves;
  if (!lost && state === "stale" && reload) {
    return { label: labels.reload, onClick: reload, disabled: fetching, busy: fetching };
  }
  if (success || state === "stale" || state === "reused") {
    return { label: labels.close, onClick: close };
  }
  return {
    label: state === "unknown" ? labels.resend : action,
    form: formId,
    disabled: pending,
    busy: pending,
    danger: !lost && danger,
  };
}

function back({ state, lost, success, pending, restart, close }: Moves, labels: { giveUp: string; cancel: string }) {
  if (success || state === "reused") return undefined;
  if (lost) {
    return state === "unknown" ? { label: labels.giveUp, onClick: restart, disabled: pending } : undefined;
  }
  return { label: labels.cancel, onClick: close };
}

function ChangeBody<Input, Out>({
  intro,
  fields,
  confirm,
  explain,
  waiting,
  change,
  problem,
}: Props<Input, Out> & { change: Change<Out>; problem?: FieldProblem }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { attempt, mutation, lost } = change;
  const editing = attempt.state === "editing";
  const notice = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // The dialog scrolls only its body. Bring a locked form's notice back beside the actions.
    if (!editing) notice.current?.scrollIntoView({ block: "nearest" });
  }, [editing]);
  const said = (
    <Said problem={problem?.message ?? null} state={attempt.state} error={mutation.error} explain={explain} />
  );
  if (lost) {
    return attempt.state === "unknown" ? (
      <FormError>
        {mutation.error ? <p>{errors.message(mutation.error)}</p> : null}
        <Text tone="muted">{t("change.unsettled")}</Text>
      </FormError>
    ) : (
      said
    );
  }
  return (
    <>
      {intro ? (
        <Text as="div" tone="muted">
          <Stack gap={8}>{intro}</Stack>
        </Text>
      ) : null}
      {editing ? said : null}
      {/* A lost answer can only resend the request already fixed from these fields. */}
      <fieldset disabled={!editing || mutation.isPending}>
        <Stack>
          <ChangeFields fields={fields} confirm={confirm} error={mutation.error} problem={problem} />
        </Stack>
      </fieldset>
      {editing ? null : <div ref={notice}>{said}</div>}
      {mutation.isPending ? waiting : null}
    </>
  );
}

function ChangeFields({
  fields,
  confirm,
  error,
  problem,
}: Pick<Props<unknown, unknown>, "fields" | "confirm"> & {
  error: unknown;
  problem?: FieldProblem;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      {fields?.(error)}
      <TextAreaField
        label={t("change.reason")}
        hint={t("change.reasonHint")}
        name="reason"
        maxLength={1000}
        error={problem?.field === "reason" ? problem.message : errors.field(error, "reason")}
      />
      {confirm === undefined ? null : (
        <TextField
          label={t("change.confirmLabel", { name: confirm })}
          name="confirm"
          autoComplete="off"
          error={problem?.field === "confirm" ? problem.message : errors.field(error, "confirm")}
        />
      )}
    </>
  );
}

// Said tells what stopped the change: a problem with what was typed, the server's refusal, or, when the dialog cannot
// simply send again, why not and what happens next.
function Said({
  problem,
  state,
  error,
  explain,
}: {
  problem: string | null;
  state: State;
  error: unknown;
  explain?: (error: unknown) => string | undefined;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (state === "stale" || state === "reused") {
    return <FormError>{t(`change.${state}`)}</FormError>;
  }
  const said = problem ?? (error ? (explain?.(error) ?? errors.message(error)) : null);
  if (state === "unknown") {
    return (
      <FormError>
        {said ? <p>{said}</p> : null}
        <Text tone="muted">{t("change.unknown")}</Text>
      </FormError>
    );
  }
  return said ? <FormError>{said}</FormError> : null;
}

// Result says what the change did. An answer that is the stored result of an earlier try is said to be one: nothing was
// done a second time.
function Result({ replayed, verdict, children }: { replayed: boolean; verdict?: boolean; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <Stack>
      {verdict ? null : (
        <div role="status">
          <Tag tone="ok" icon="check">
            {t("change.done")}
          </Tag>
        </div>
      )}
      {replayed ? (
        <div role="status">
          <Tag>{t("change.replayed")}</Tag>
        </div>
      ) : null}
      <Text as="div" tone="muted">
        <Stack gap={8}>{children}</Stack>
      </Text>
    </Stack>
  );
}
