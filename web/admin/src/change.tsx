import { ApiError } from "@axiom/api";
import { accountKey, readForm, useErrors } from "@axiom/app";
import { Button, Dialog, FormError, Icon, SubmitButton, TextAreaField, TextField } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

// A change is one write of the admin API, which the person does for a reason that goes to the audit log, and which the
// server does at most once per Idempotency-Key (api/admin.yaml). ChangeDialog is the one way a page makes it: the
// fields of the change, the reason, a button that says what it does (and, for a risky change, a name to type first),
// and afterwards what happened, including when the answer is the stored result of an earlier try.

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
};

// A refused request (4xx) will be refused again with the same key and body, and the person may now change the body:
// they get a new key. A request that may have been done (no response, or a server failure) keeps its key, so sending
// it again cannot do it twice.
function mayHaveBeenDone(error: unknown) {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

// One key per intended change, made when the dialog opens: pressing the button again, or sending after a lost
// response, is the same change and not a second one. A change that was done refreshes everything the admin shows.
function useChange<Input, Out>(send: Props<Input, Out>["send"]) {
  const queryClient = useQueryClient();
  const key = useRef(crypto.randomUUID());
  return useMutation({
    mutationFn: ({ input, reason }: { input: Input; reason: string }) => send(input, { reason, key: key.current }),
    onError: (error) => {
      if (!mayHaveBeenDone(error)) {
        key.current = crypto.randomUUID();
      }
      // Someone changed the thing since it was read: show what it is now, so the next try is made against that.
      if (error instanceof ApiError && error.code === "stale_revision") {
        void queryClient.invalidateQueries({ queryKey: [accountKey] });
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [accountKey] }),
  });
}

export function ChangeDialog<Input, Out>(props: Props<Input, Out>) {
  const { send, done, onClose, title } = props;
  const change = useChange<Input, Out>(send);
  return (
    <Dialog title={title} onClose={onClose}>
      {change.isSuccess ? (
        <Result replayed={change.data.replayed} onClose={onClose}>
          {done(change.data.data)}
        </Result>
      ) : (
        <ChangeForm {...props} change={change} />
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
  onClose,
  change,
}: Props<Input, Out> & { change: ReturnType<typeof useChange<Input, Out>> }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [problem, setProblem] = useState<string | null>(null);
  const refusal = (values: Record<string, string>, reason: string) => {
    if (!reason) {
      return t("change.reasonMissing");
    }
    return confirm !== undefined && values.confirm !== confirm ? t("change.confirmMismatch") : null;
  };
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const values = readForm(event.currentTarget);
        const reason = (values.reason ?? "").trim();
        const input = read(values);
        const found = isProblem(input) ? input.problem : refusal(values, reason);
        setProblem(found);
        if (found === null && !change.isPending) {
          change.mutate({ input: input as Input, reason });
        }
      }}
    >
      {intro ? <div className="space-y-2 text-t2">{intro}</div> : null}
      {problem || change.isError ? <FormError>{problem ?? errors.message(change.error)}</FormError> : null}
      {fields?.(change.error)}
      <TextAreaField
        label={t("change.reason")}
        hint={t("change.reasonHint")}
        name="reason"
        rows={2}
        maxLength={1000}
        className="font-sans"
        error={errors.field(change.error, "reason")}
      />
      {confirm === undefined ? null : (
        <TextField
          label={t("change.confirmLabel", { name: confirm })}
          name="confirm"
          autoComplete="off"
          spellCheck={false}
        />
      )}
      <div className="flex justify-end gap-2 pt-2">
        <Button size="md" onClick={onClose}>
          {t("cancel", { ns: "common" })}
        </Button>
        <SubmitButton pending={change.isPending} variant={danger ? "danger" : "primary"}>
          {action}
        </SubmitButton>
      </div>
    </form>
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
