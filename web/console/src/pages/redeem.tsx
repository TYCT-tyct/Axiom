import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import {
  Button,
  Card,
  Cluster,
  DataTable,
  Field,
  FormError,
  Note,
  Stack,
  SubmitButton,
  useFormat,
  usePageTitle,
} from "@axiom/ui";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery, redemptionsQuery } from "../queries";
import {
  afterFailure,
  type RedeemRequest,
  type Redemption,
  readCode,
  readRedemption,
  type Sendable,
  writeRedemption,
} from "../redeem";
import { useBillAttempt } from "./bill-attempt";
import { BillPagedList, BillShell } from "./bill-shell";

type RedeemedResult = Schemas["RedeemCodeRedemption"];
type RedemptionRow = Schemas["RedeemCodeRedemption"];

type RedeemState = {
  attempt: Redemption;
  pending: boolean;
  failure?: unknown;
  redeemed?: RedeemedResult;
};

type RedeemSubmission = { from: Sendable; request: RedeemRequest };

function useRedeemController(scope: string) {
  const queryClient = useQueryClient();
  const { state, start, finish, change } = useBillAttempt<RedeemState>(scope, "redeem", () => ({
    attempt: readRedemption(scope) ?? { state: "editing", key: crypto.randomUUID() },
    pending: false,
  }));
  // A code stays with the form that received it. A new route can observe the key and the outcome.
  const local = useRef<{ key: string; request: RedeemRequest } | null>(null);

  const submit = useMutation({
    mutationFn: ({ from, request }: RedeemSubmission) =>
      unwrap(
        consoleApi.POST("/api/console/v1/redeem-codes", {
          body: request,
          params: { header: { "Idempotency-Key": from.key } },
        }),
      ),
    onSuccess: (redeemed, { from }) => {
      const committed = finish(from.key, (current) => {
        const attempt = { state: "editing" as const, key: crypto.randomUUID() };
        writeRedemption(scope, attempt);
        return { ...current, attempt, redeemed };
      });
      if (committed) {
        void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
        void queryClient.invalidateQueries({ queryKey: redemptionsQuery.queryKey });
      }
    },
    onError: (failure, { from, request }) => {
      finish(from.key, (current) => {
        const next = afterFailure(from, request, failure, () => crypto.randomUUID());
        const attempt = next.state === "unknown" ? { state: "unknown" as const, key: next.key } : next;
        writeRedemption(scope, attempt);
        return { ...current, attempt, failure };
      });
    },
  });

  const reset = () =>
    change((current) => {
      if (current.pending) return undefined;
      const attempt = { state: "editing" as const, key: crypto.randomUUID() };
      writeRedemption(scope, attempt);
      local.current = null;
      return { attempt, pending: false };
    });

  const send = ({ from, request }: RedeemSubmission) => {
    if (
      start(from.key, (current) => {
        writeRedemption(scope, { state: "unknown", key: from.key });
        return { ...current, failure: undefined };
      })
    ) {
      local.current = { key: from.key, request };
      submit.mutate({ from, request });
    }
  };

  const { attempt, failure, redeemed, pending } = state;
  const redemption =
    attempt.state === "unknown" && local.current?.key === attempt.key
      ? { ...attempt, request: local.current.request }
      : attempt;
  return { redemption, failure, redeemed, pending, send, reset };
}

function RedeemedCard({ redeemed, onAgain }: { redeemed: RedeemedResult; onAgain: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Card title={t("redeem.redeemed")}>
      <Stack gap={12}>
        <p>{t("redeem.redeemedBody", { amount: format.money(redeemed.amount), hint: redeemed.code_hint })}</p>
        <span>
          {redeemed.expires_at
            ? t("redeem.redeemedExpires", { at: format.dateTime(new Date(redeemed.expires_at)) })
            : t("redeem.redeemedNever")}
        </span>
        <Cluster gap={8}>
          <Button variant="secondary" onClick={onAgain}>
            {t("redeem.again")}
          </Button>
        </Cluster>
      </Stack>
    </Card>
  );
}

function RedeemStatusNotice({ redemption }: { redemption: Redemption }) {
  const { t } = useTranslation();
  if (redemption.state === "reused") return <FormError>{t("redeem.reused")}</FormError>;
  if (redemption.state === "unknown") {
    return <Note title={t(redemption.request ? "redeem.unknown" : "redeem.unknownAgain")} />;
  }
  return null;
}

function RedeemSubmitRow({
  redemption,
  pending,
  onRestart,
}: {
  redemption: Redemption;
  pending: boolean;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const isUnknown = redemption.state === "unknown";
  const isReused = redemption.state === "reused";
  return (
    <Cluster gap={8}>
      {!isReused ? (
        <SubmitButton pending={pending}>{isUnknown ? t("redeem.resend") : t("redeem.submit")}</SubmitButton>
      ) : null}
      {isUnknown || isReused ? (
        <Button variant="ghost" disabled={pending} onClick={onRestart}>
          {t("redeem.restart")}
        </Button>
      ) : null}
    </Cluster>
  );
}

function RedeemForm({
  code,
  problem,
  failure,
  redemption,
  pending,
  onCodeChange,
  onSubmit,
  onRestart,
}: {
  code: string;
  problem: "missing" | "long" | null;
  failure: unknown;
  redemption: Redemption;
  pending: boolean;
  onCodeChange: (value: string) => void;
  onSubmit: () => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const asking = redemption.state === "editing" || (redemption.state === "unknown" && !redemption.request);
  const fieldError = problem === "long" ? t("redeem.tooLong") : failure ? errors.field(failure, "code") : undefined;
  const formError = failure ? errors.message(failure) : undefined;
  return (
    <Card title={t("redeem.title")} lede={t("redeem.intro")}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!pending) onSubmit();
        }}
      >
        <Stack gap={12}>
          <RedeemStatusNotice redemption={redemption} />
          <Field
            label={t("redeem.code")}
            hint={t("redeem.codeHint")}
            value={code}
            disabled={!asking || pending}
            spellCheck={false}
            autoComplete="off"
            maxLength={64}
            error={fieldError}
            onChange={(event) => onCodeChange(event.target.value)}
          />
          {problem === "missing" ? <FormError>{t("redeem.missing")}</FormError> : null}
          {formError ? <FormError>{formError}</FormError> : null}
          <RedeemSubmitRow redemption={redemption} pending={pending} onRestart={onRestart} />
        </Stack>
      </form>
    </Card>
  );
}

function submitRedemption({
  code,
  redemption,
  pending,
  send,
  onProblem,
}: {
  code: string;
  redemption: Redemption;
  pending: boolean;
  send: (vars: { from: Sendable; request: RedeemRequest }) => void;
  onProblem: (next: "missing" | "long" | null) => void;
}) {
  if (pending) return;
  if (redemption.state === "unknown" && redemption.request) {
    onProblem(null);
    send({ from: redemption, request: redemption.request });
    return;
  }
  const parsed = readCode({ code });
  if (parsed === "missing" || parsed === "long") return onProblem(parsed);
  onProblem(null);
  if (redemption.state === "editing" || redemption.state === "unknown") {
    send({ from: redemption, request: parsed });
  }
}

function RedeemFormCard({ allowed }: { allowed: boolean }) {
  const { t } = useTranslation();
  const session = useSession();
  const scope = session.account ? `${session.user.id}/${session.account}` : session.user.id;
  const ctrl = useRedeemController(scope);
  const [code, setCode] = useState("");
  const [problem, setProblem] = useState<"missing" | "long" | null>(null);

  if (!allowed) {
    return (
      <Card title={t("redeem.title")} lede={t("redeem.intro")}>
        <Note title={t("redeem.forbidden")} />
      </Card>
    );
  }
  if (ctrl.redeemed) {
    return (
      <RedeemedCard
        redeemed={ctrl.redeemed}
        onAgain={() => {
          setCode("");
          setProblem(null);
          ctrl.reset();
        }}
      />
    );
  }
  return (
    <RedeemForm
      code={code}
      problem={problem}
      failure={ctrl.failure}
      redemption={ctrl.redemption}
      pending={ctrl.pending}
      onCodeChange={(value) => {
        setCode(value);
        if (problem) setProblem(null);
      }}
      onSubmit={() =>
        submitRedemption({
          code,
          redemption: ctrl.redemption,
          pending: ctrl.pending,
          send: ctrl.send,
          onProblem: setProblem,
        })
      }
      onRestart={() => {
        setProblem(null);
        ctrl.reset();
      }}
    />
  );
}

function RedeemHistoryCard() {
  const { t } = useTranslation();
  const format = useFormat();
  const query = useInfiniteQuery(redemptionsQuery);
  return (
    <BillPagedList
      query={query}
      title={t("redeem.history")}
      emptyTitle={t("redeem.empty")}
      emptyBody={t("redeem.emptyBody")}
      renderTable={(rows) => (
        <DataTable<RedemptionRow>
          label={t("redeem.history")}
          rows={rows}
          rowKey={(row) => row.id}
          columns={[
            { key: "code", header: t("redeem.col.code"), cell: (row) => row.code_hint },
            {
              key: "amount",
              header: t("redeem.col.amount"),
              align: "end",
              cell: (row) => format.money(row.amount),
            },
            {
              key: "redeemed",
              header: t("redeem.col.redeemed"),
              cell: (row) => format.dateTime(new Date(row.redeemed_at)),
            },
            {
              key: "expires",
              header: t("redeem.col.expires"),
              cell: (row) => (row.expires_at ? format.dateTime(new Date(row.expires_at)) : t("redeem.never")),
            },
          ]}
        />
      )}
    />
  );
}

export default function RedeemPage() {
  const { t } = useTranslation();
  const allowed = can(useSession(), "balance.top_up");
  usePageTitle(t("nav.redeem"));

  return (
    <BillShell activeTab="redeem" title={t("redeem.title")} subtitle={t("redeem.description")}>
      <Stack gap={16}>
        <RedeemFormCard allowed={allowed} />
        <RedeemHistoryCard />
      </Stack>
    </BillShell>
  );
}
