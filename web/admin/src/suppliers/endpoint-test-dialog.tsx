import { type AdminSchemas, ApiError, adminApi, fetchWithin, unwrapChange } from "@axiom/api";
import { CopyButton, Icon, type IconName, SelectField, useFormat } from "@axiom/ui";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { Code } from "../parts";
import { duration, testFormats } from "./endpoint-test";

type Endpoint = AdminSchemas["SupplierEndpoint"];
type Result = AdminSchemas["EndpointTest"];
type Format = AdminSchemas["TestFormat"];

// The server gives a test 45 seconds of generation and the whole operation a minute (api/admin.yaml); the call waits a
// little longer than that, so the answer of a test that ran to its limit still arrives. The client's usual ten seconds
// would end every slow test without its answer, leaving the dialog to resend it (which replays, but only once it is done).
const testLimit = 45;
const callTimeout = 75_000;

// A test is a change in the dialog's sense: one Idempotency-Key per opened dialog, so pressing again or resending after
// a lost answer replays the first test's result instead of paying for a second generation (change.tsx).
export function EndpointTestDialog({
  endpoint,
  group,
  onClose,
}: {
  endpoint: Endpoint;
  group: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const formats = testFormats(endpoint.formats);
  return (
    <ChangeDialog
      operation={`endpoint-test:${endpoint.id}`}
      title={t("suppliers.test.title", { name: endpoint.upstream_name, group })}
      intro={<p>{t("suppliers.test.body")}</p>}
      action={t("suppliers.test.action")}
      fields={() => (
        <SelectField label={t("suppliers.test.format")} name="format" defaultValue={formats[0]}>
          {formats.map((format) => (
            <option key={format} value={format}>
              {t(`suppliers.test.format_${format}`)}
            </option>
          ))}
        </SelectField>
      )}
      read={(values) => values.format as Format}
      send={(format, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/endpoint-tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { endpoint_id: endpoint.id, format, reason },
            fetch: fetchWithin(callTimeout),
          }),
        )
      }
      waiting={<Waiting />}
      explain={(error) =>
        error instanceof ApiError && (error.code === "conflict" || error.code === "unprocessable")
          ? t(`suppliers.test.refused.${error.code}`)
          : undefined
      }
      verdict
      done={(result) => <Verdict result={result} />}
      onClose={onClose}
    />
  );
}

// Waiting shows the time the test has taken against its limit. The bar is a deadline, not progress: a test that
// answers in two seconds ends far short of it. The count is not announced every second; the message is, once.
function Waiting() {
  const { t } = useTranslation();
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    const started = Date.now();
    const timer = setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 1000);
    return () => clearInterval(timer);
  }, []);
  return (
    <div className="space-y-2 rounded-btn bg-s2 px-3 py-2.5">
      <div className="flex items-start justify-between gap-3">
        <p role="status" className="text-t2">
          {t("suppliers.test.waiting")}
        </p>
        <span aria-hidden className="shrink-0 font-mono text-label text-t3 tabular-nums">
          {t("suppliers.test.elapsed", { seconds })}
        </span>
      </div>
      <div aria-hidden className="h-1 overflow-hidden rounded-full bg-s3">
        <div
          className="h-full origin-left bg-accent transition-transform duration-1000 ease-linear motion-reduce:transition-none"
          style={{ transform: `scaleX(${Math.min(seconds / testLimit, 1)})` }}
        />
      </div>
    </div>
  );
}

const outcomes = {
  passed: { icon: "check", box: "bg-ok-soft", mark: "text-ok" },
  failed: { icon: "close", box: "bg-bad-soft", mark: "text-bad" },
  inconclusive: { icon: "alert", box: "bg-warn-soft", mark: "text-warn" },
} as const satisfies Record<Result["outcome"], { icon: IconName; box: string; mark: string }>;

// Verdict is what the test found, first and in words, then the facts to look it up by: the timings, the cost booked
// and both request IDs.
function Verdict({ result }: { result: Result }) {
  const { t } = useTranslation();
  const format = useFormat();
  const look = outcomes[result.outcome];
  const cause = result.problem ? t(`suppliers.test.cause.${result.problem.cause}`) : "";
  const scope = t(`suppliers.test.scope.${result.problem?.scope ?? "own"}`);
  const status = result.upstream_status ? String(result.upstream_status) : t("suppliers.test.noAnswer");
  return (
    <div className="space-y-4 text-t1">
      <div role="status" className={`flex items-start gap-3 rounded-btn px-3 py-3 ${look.box}`}>
        <Icon name={look.icon} width="20" height="20" className={`mt-0.5 shrink-0 ${look.mark}`} />
        <div className="min-w-0 space-y-0.5">
          <p className="text-[15px] font-medium">{t(`suppliers.test.outcome.${result.outcome}.title`)}</p>
          <p className="text-t2 [overflow-wrap:anywhere]">
            {t(`suppliers.test.outcome.${result.outcome}.body`, { cause, scope })}
          </p>
        </div>
      </div>
      <dl className="divide-y divide-b1 rounded-btn border border-b1">
        <Row label={t("suppliers.test.model")}>
          <Code>{result.upstream_model}</Code>
          <span className="ml-2 inline-block rounded-xs bg-s3 px-1.5 py-0.5 text-label text-t2">
            {t(`suppliers.test.format_${result.format}`)}
          </span>
        </Row>
        <Row label={t("suppliers.test.status")}>{status}</Row>
        <Row label={t("suppliers.test.firstToken")}>
          {result.first_token_ms === undefined ? t("suppliers.test.none") : <Duration ms={result.first_token_ms} />}
        </Row>
        <Row label={t("suppliers.test.total")}>
          <Duration ms={result.total_ms} />
          <Timeline first={result.first_token_ms} total={result.total_ms} />
        </Row>
        <Row label={t("suppliers.test.cost")}>
          {t("suppliers.balanceNative", {
            value: format.decimal(result.estimated_native),
            unit: t(`suppliers.native.${result.native_unit}`),
          })}
          <p className="text-label text-t4">{t("suppliers.test.costHint")}</p>
        </Row>
        <Row label={t("suppliers.test.requestId")}>
          <Copyable text={result.request_id} label={t("suppliers.test.copyRequestId")} />
        </Row>
        <Row label={t("suppliers.test.upstreamRequestId")}>
          {result.upstream_request_id ? (
            <Copyable text={result.upstream_request_id} label={t("suppliers.test.copyUpstreamRequestId")} />
          ) : (
            t("suppliers.test.none")
          )}
        </Row>
      </dl>
    </div>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-4 gap-y-1 px-3 py-2.5 sm:grid-cols-[9rem_minmax(0,1fr)]">
      <dt className="text-t3">{label}</dt>
      <dd className="min-w-0 [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

function Duration({ ms }: { ms: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { unit, value } = duration(ms);
  return <span className="tabular-nums">{t(`suppliers.test.${unit}`, { value: format.number(value) })}</span>;
}

// Timeline draws the wait for the first content against the whole test, so a slow start and a slow stream look
// different at a glance; the numbers beside it say the same in words.
function Timeline({ first, total }: { first: number | undefined; total: number }) {
  if (first === undefined || total <= 0) {
    return null;
  }
  return (
    <div aria-hidden className="mt-1.5 h-1.5 max-w-64 overflow-hidden rounded-full bg-accent-soft">
      <div className="h-full rounded-full bg-accent" style={{ width: `${Math.min((first / total) * 100, 100)}%` }} />
    </div>
  );
}

function Copyable({ text, label }: { text: string; label: string }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <Code>{text}</Code>
      <CopyButton text={text} label={label} />
    </div>
  );
}
