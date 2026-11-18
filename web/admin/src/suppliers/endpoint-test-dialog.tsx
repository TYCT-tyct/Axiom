import { type AdminSchemas, ApiError, adminApi, fetchWithin, unwrapChange } from "@axiom/api";
import { TestProgress } from "@axiom/boards/ds";
import { Cluster, CopyButton, Facts, Select, Stack, Tag, Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { Code, State } from "../parts";
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
      intro={<Text>{t("suppliers.test.body")}</Text>}
      action={t("suppliers.test.action")}
      fields={() => (
        <Select label={t("suppliers.test.format")} name="format" defaultValue={formats[0]}>
          {formats.map((format) => (
            <option key={format} value={format}>
              {t(`suppliers.test.format_${format}`)}
            </option>
          ))}
        </Select>
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
  return (
    <TestProgress
      label={t("suppliers.test.waiting")}
      limitSeconds={testLimit}
      formatElapsed={(seconds) => t("suppliers.test.elapsed", { seconds })}
    />
  );
}

const outcomes = { passed: "good", failed: "bad", inconclusive: "warn" } as const satisfies Record<
  Result["outcome"],
  "good" | "warn" | "bad"
>;

// Verdict is what the test found, first and in words, then the facts to look it up by: the timings, the cost booked
// and both request IDs.
function Verdict({ result }: { result: Result }) {
  const { t } = useTranslation();
  const cause = result.problem ? t(`suppliers.test.cause.${result.problem.cause}`) : "";
  const scope = t(`suppliers.test.scope.${result.problem?.scope ?? "own"}`);
  const status = result.upstream_status ? String(result.upstream_status) : t("suppliers.test.noAnswer");
  return (
    <Stack>
      <div role="status">
        <Stack gap={8}>
          <State tone={outcomes[result.outcome]}>{t(`suppliers.test.outcome.${result.outcome}.title`)}</State>
          <Text tone="muted">{t(`suppliers.test.outcome.${result.outcome}.body`, { cause, scope })}</Text>
        </Stack>
      </div>
      <Facts
        rows={[
          [
            t("suppliers.test.model"),
            <Cluster key="model" gap={8}>
              <Code>{result.upstream_model}</Code>
              <Tag>{t(`suppliers.test.format_${result.format}`)}</Tag>
            </Cluster>,
          ],
          [t("suppliers.test.status"), status],
          [
            t("suppliers.test.firstToken"),
            result.first_token_ms === undefined ? (
              t("suppliers.test.none")
            ) : (
              <Duration key="first" ms={result.first_token_ms} />
            ),
          ],
          [t("suppliers.test.total"), <Duration key="total" ms={result.total_ms} />],
          [t("suppliers.test.cost"), <TestCost key="cost" result={result} />],
          [
            t("suppliers.test.requestId"),
            <Copyable key="request" text={result.request_id} label={t("suppliers.test.copyRequestId")} />,
          ],
          [
            t("suppliers.test.upstreamRequestId"),
            result.upstream_request_id ? (
              <Copyable
                key="upstream"
                text={result.upstream_request_id}
                label={t("suppliers.test.copyUpstreamRequestId")}
              />
            ) : (
              t("suppliers.test.none")
            ),
          ],
        ]}
      />
    </Stack>
  );
}

function Duration({ ms }: { ms: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { unit, value } = duration(ms);
  return <>{t(`suppliers.test.${unit}`, { value: format.number(value) })}</>;
}

function Copyable({ text, label }: { text: string; label: string }) {
  return (
    <Cluster>
      <Code>{text}</Code>
      <CopyButton text={text} label={label} />
    </Cluster>
  );
}

// TestCost is what the test is estimated to have cost on the supplier, in its own unit, and that it is an estimate.
function TestCost({ result }: { result: Result }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Stack gap={4}>
      <Text>
        {t("suppliers.balanceNative", {
          value: format.decimal(result.estimated_native),
          unit: t(`suppliers.native.${result.native_unit}`),
        })}
      </Text>
      <Text size="sm" tone="faint">
        {t("suppliers.test.costHint")}
      </Text>
    </Stack>
  );
}
