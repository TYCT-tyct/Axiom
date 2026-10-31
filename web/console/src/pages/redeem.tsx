import { consoleApi, type Schemas, unwrapWithStatus } from "@axiom/api";
import {
  type Attempt,
  can,
  failedFrom,
  LoadMore,
  opened,
  readForm,
  type Sendable,
  useErrors,
  useSession,
} from "@axiom/app";
import {
  Button,
  DataState,
  EmptyState,
  FormError,
  PageHeader,
  Panel,
  SubmitButton,
  Table,
  Td,
  TextField,
  Th,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { balanceQuery, redemptionsQuery } from "../queries";
import { isRedeemRequest, type RedeemRequest, readCode } from "../redeem";

// The redeem page turns a code into credit and lists what this account redeemed before. A code is shown once by the
// person who hands it out; the page never reads it back, and the history carries its last four characters only.
export default function Redeem() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("nav.redeem")} description={t("redeem.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <RedeemPanel />
        <History />
      </div>
    </>
  );
}

// attemptStore keeps the key and the code across a reload while the answer is unknown, so a retry cannot redeem twice.
const attemptStore = "axiom.console.redeem.attempt";
const attemptKey = (scope: string) => `${scope}/${attemptStore}`;

// useRedeemRequest is the redemption being made: the code, and the key that makes a retry the same redemption. The
// code and its key are fixed while the answer is unknown, also after a reload; a refusal is the only answer that
// allows a new code.
function useRedeemRequest(onRedeemed: (redemption: Schemas["RedeemCodeRedemption"]) => void) {
  const client = useQueryClient();
  const { user, account } = useSession();
  const scope = `${user.id}/${account}/redeem`;
  const [initial] = useState(() => readAttempt(attemptKey(scope), isRedeemRequest));
  const [attempt, setAttempt] = useState<Attempt<RedeemRequest>>(() => initial ?? opened(crypto.randomUUID()));
  const [failure, setFailure] = useState<unknown>(null);
  const change = (next: Attempt<RedeemRequest>) => {
    setAttempt(next);
    writeAttempt(attemptKey(scope), next);
  };
  type Sent = { from: Sendable<RedeemRequest>; request: RedeemRequest };
  const redeem = useMutation({
    // Recorded before it is sent: a lost answer leaves only this request under this key to send again.
    onMutate: ({ from, request }: Sent) => change(unanswered(from, request)),
    mutationFn: ({ from, request }: Sent) =>
      unwrapWithStatus(
        consoleApi.POST("/api/console/v1/redeem-codes", {
          params: { header: { "Idempotency-Key": from.key } },
          body: request,
        }),
      ),
    onSuccess: ({ data }) => {
      clearAttempt(attemptKey(scope));
      setFailure(null);
      onRedeemed(data);
      change(opened(crypto.randomUUID()));
      void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      void client.invalidateQueries({ queryKey: redemptionsQuery.queryKey });
    },
    onError: failedFrom(change, setFailure),
  });
  return { attempt, failure, setFailure, redeeming: redeem.isPending, redeem, change };
}

function RedeemPanel() {
  const { t } = useTranslation();
  const allowed = can(useSession(), "balance.top_up");
  const [done, setDone] = useState<Schemas["RedeemCodeRedemption"] | null>(null);
  const { attempt, failure, setFailure, redeeming, redeem, change } = useRedeemRequest(setDone);
  const [problem, setProblem] = useState<"missing" | "long" | null>(null);
  const submit = (form: HTMLFormElement) => {
    if (redeeming || attempt.state === "reused" || attempt.state === "stale") {
      return;
    }
    if (attempt.state === "unknown") {
      setProblem(null);
      redeem.mutate({ from: attempt, request: attempt.request });
      return;
    }
    const read = readCode(readForm(form));
    if (read === "missing" || read === "long") {
      setFailure(null);
      setProblem(read);
      return;
    }
    setProblem(null);
    redeem.mutate({ from: attempt, request: read });
  };
  if (!allowed) {
    return (
      <Panel title={t("redeem.title")}>
        <p className="px-4 py-3 text-t3">{t("redeem.forbidden")}</p>
      </Panel>
    );
  }
  if (done) {
    return <Redeemed redemption={done} onAgain={() => setDone(null)} />;
  }
  return (
    <Panel title={t("redeem.title")}>
      <CodeForm
        attempt={attempt}
        failure={failure}
        problem={problem}
        pending={redeeming}
        onSubmit={submit}
        onRestart={() => change(opened(crypto.randomUUID()))}
      />
    </Panel>
  );
}

// CodeForm asks for one code and says where the attempt stands. A code is shown once, by whoever hands it out, so the
// field is never filled from what was sent.
function CodeForm({
  attempt,
  failure,
  problem,
  pending,
  onSubmit,
  onRestart,
}: {
  attempt: Attempt<RedeemRequest>;
  failure: unknown;
  problem: "missing" | "long" | null;
  pending: boolean;
  onSubmit: (form: HTMLFormElement) => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const editing = attempt.state === "editing";
  return (
    <form
      noValidate
      className="space-y-4 p-4"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(event.currentTarget);
      }}
    >
      <p className="max-w-prose text-ui text-t2">{t("redeem.intro")}</p>
      <TextField
        label={t("redeem.code")}
        hint={t("redeem.codeHint")}
        name="code"
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
        disabled={!editing}
        error={problem === "long" ? t("redeem.tooLong") : errors.field(failure, "code")}
      />
      {problem === "missing" ? <FormError>{t("redeem.missing")}</FormError> : null}
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {attempt.state === "unknown" ? <p className="text-label text-t3">{t("redeem.unknown")}</p> : null}
      {attempt.state === "reused" ? <p className="text-label text-t3">{t("redeem.reused")}</p> : null}
      <div className="flex justify-end gap-2">
        {editing ? null : (
          <Button size="md" onClick={onRestart} disabled={pending}>
            {t("redeem.restart")}
          </Button>
        )}
        {attempt.state === "editing" || attempt.state === "unknown" ? (
          <SubmitButton pending={pending}>
            {attempt.state === "unknown" ? t("redeem.resend") : t("redeem.submit")}
          </SubmitButton>
        ) : null}
      </div>
    </form>
  );
}

function Redeemed({ redemption, onAgain }: { redemption: Schemas["RedeemCodeRedemption"]; onAgain: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Panel title={t("redeem.redeemed")}>
      <div className="space-y-3 p-4">
        <p className="text-t1">
          {t("redeem.redeemedBody", { amount: format.money(redemption.amount), hint: redemption.code_hint })}
        </p>
        <p className="text-ui text-t3">
          {redemption.expires_at
            ? t("redeem.redeemedExpires", { at: format.dateTime(new Date(redemption.expires_at)) })
            : t("redeem.redeemedNever")}
        </p>
        <Button size="md" onClick={onAgain}>
          {t("redeem.again")}
        </Button>
      </div>
    </Panel>
  );
}

function History() {
  const { t } = useTranslation();
  const history = useInfiniteQuery(redemptionsQuery);
  const rows = history.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <Panel title={t("redeem.history")}>
      <DataState
        query={history}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("redeem.empty")}>{t("redeem.emptyBody")}</EmptyState>}
      >
        {() => <HistoryTable rows={rows} />}
      </DataState>
      <LoadMore list={history} />
    </Panel>
  );
}

function HistoryTable({ rows }: { rows: Schemas["RedeemCodeRedemption"][] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Table label={t("redeem.history")}>
      <thead>
        <tr>
          <Th>{t("redeem.col.code")}</Th>
          <Th className="text-right">{t("redeem.col.amount")}</Th>
          <Th>{t("redeem.col.redeemed")}</Th>
          <Th>{t("redeem.col.expires")}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.id} className="hover:bg-s2">
            <Td>
              <span className="font-mono text-label">
                {"…"}
                {row.code_hint}
              </span>
            </Td>
            <Td className="whitespace-nowrap text-right tabular-nums">{format.money(row.amount)}</Td>
            <Td className="whitespace-nowrap">{format.dateTime(new Date(row.redeemed_at))}</Td>
            <Td className="whitespace-nowrap">
              {row.expires_at ? format.dateTime(new Date(row.expires_at)) : t("redeem.never")}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
