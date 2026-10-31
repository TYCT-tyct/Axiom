import { consoleApi, type Schemas, unwrapWithStatus } from "@axiom/api";
import { can, LoadMore, readForm, useErrors, useSession } from "@axiom/app";
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

// useRedeemRequest is the redemption being made: the code in this page's memory and the key that makes a retry the
// same redemption. Only the key of an answer that did not arrive survives a reload, and the form asks for the code
// again under it; a refusal is the only answer that allows a new key.
function useRedeemRequest(onRedeemed: (redemption: Schemas["RedeemCodeRedemption"]) => void) {
  const client = useQueryClient();
  const { user, account } = useSession();
  const scope = `${user.id}/${account}/redeem`;
  const [initial] = useState(() => readRedemption(scope));
  const [redemption, setRedemption] = useState<Redemption>(
    () => initial ?? { state: "editing", key: crypto.randomUUID() },
  );
  const [failure, setFailure] = useState<unknown>(null);
  const change = (next: Redemption) => {
    setRedemption(next);
    writeRedemption(scope, next);
  };
  type Sent = { from: Sendable; request: RedeemRequest };
  const redeem = useMutation({
    // Recorded before it is sent: a lost answer leaves this key to send the code again.
    onMutate: ({ from, request }: Sent) => change({ state: "unknown", key: from.key, request }),
    mutationFn: ({ from, request }: Sent) =>
      unwrapWithStatus(
        consoleApi.POST("/api/console/v1/redeem-codes", {
          params: { header: { "Idempotency-Key": from.key } },
          body: request,
        }),
      ),
    onSuccess: ({ data }) => {
      setFailure(null);
      onRedeemed(data);
      change({ state: "editing", key: crypto.randomUUID() });
      void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      void client.invalidateQueries({ queryKey: redemptionsQuery.queryKey });
    },
    onError: (error: unknown, sent: Sent) => {
      setFailure(error);
      change(afterFailure(sent.from, sent.request, error, () => crypto.randomUUID()));
    },
  });
  return { redemption, failure, setFailure, redeeming: redeem.isPending, redeem, change };
}

function RedeemPanel() {
  const { t } = useTranslation();
  const allowed = can(useSession(), "balance.top_up");
  const [done, setDone] = useState<Schemas["RedeemCodeRedemption"] | null>(null);
  const { redemption, failure, setFailure, redeeming, redeem, change } = useRedeemRequest(setDone);
  const [problem, setProblem] = useState<"missing" | "long" | null>(null);
  const submit = (form: HTMLFormElement) => {
    if (redeeming || redemption.state === "reused") {
      return;
    }
    // A code this page still holds goes out again as it was sent; after a reload it is asked for again, under the key
    // that came back from storage.
    if (redemption.state === "unknown" && redemption.request) {
      setProblem(null);
      redeem.mutate({ from: redemption, request: redemption.request });
      return;
    }
    const read = readCode(readForm(form));
    if (read === "missing" || read === "long") {
      setFailure(null);
      setProblem(read);
      return;
    }
    setProblem(null);
    redeem.mutate({ from: redemption, request: read });
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
        redemption={redemption}
        failure={failure}
        problem={problem}
        pending={redeeming}
        onSubmit={submit}
        onRestart={() => change({ state: "editing", key: crypto.randomUUID() })}
      />
    </Panel>
  );
}

// CodeForm asks for one code and says where the redemption stands. A code is shown once, by whoever hands it out, so
// the field is never filled from what was sent; after a reload of an unanswered attempt it is asked for again.
function CodeForm({
  redemption,
  failure,
  problem,
  pending,
  onSubmit,
  onRestart,
}: {
  redemption: Redemption;
  failure: unknown;
  problem: "missing" | "long" | null;
  pending: boolean;
  onSubmit: (form: HTMLFormElement) => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const asking = redemption.state === "editing" || (redemption.state === "unknown" && !redemption.request);
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
        disabled={!asking}
        error={problem === "long" ? t("redeem.tooLong") : errors.field(failure, "code")}
      />
      {problem === "missing" ? <FormError>{t("redeem.missing")}</FormError> : null}
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {redemption.state === "unknown" ? (
        <p className="text-label text-t3">{redemption.request ? t("redeem.unknown") : t("redeem.unknownAgain")}</p>
      ) : null}
      {redemption.state === "reused" ? <p className="text-label text-t3">{t("redeem.reused")}</p> : null}
      <CodeActions redemption={redemption} pending={pending} onRestart={onRestart} />
    </form>
  );
}

// CodeActions offers what the state allows: a new attempt once the old one is known to be over, and a send while the
// code can still be typed.
function CodeActions({
  redemption,
  pending,
  onRestart,
}: {
  redemption: Redemption;
  pending: boolean;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex justify-end gap-2">
      {redemption.state === "editing" ? null : (
        <Button size="md" onClick={onRestart} disabled={pending}>
          {t("redeem.restart")}
        </Button>
      )}
      {redemption.state === "editing" || redemption.state === "unknown" ? (
        <SubmitButton pending={pending}>
          {redemption.state === "unknown" ? t("redeem.resend") : t("redeem.submit")}
        </SubmitButton>
      ) : null}
    </div>
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
