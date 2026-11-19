import type { Schemas } from "@axiom/api";
import { QrCode } from "@axiom/app";
import { Badge, Button, Cluster, CodeBlock, CopyButton, Icon, Stack, Text, TextLink, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

// OrderStatus is where one order stands: what it is, then how to pay it while it waits, or what became of it.
export function OrderStatus({
  order,
  verifying,
  cancelling,
  onVerify,
  onCancel,
  onRestart,
}: {
  order: Schemas["Order"];
  verifying: boolean;
  cancelling: boolean;
  onVerify: () => void;
  onCancel: () => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      <Cluster gap={12}>
        <Badge tone={tone(order.state)} icon={order.state === "paid" ? "check" : "refresh"}>
          {t(`orders.state.${order.state}`)}
        </Badge>
        <Text as="span" tone="muted">
          {t("topup.summary", { credits: format.money(order.credits), total: format.pay(order.total) })}
        </Text>
      </Cluster>
      {order.state === "pending" ? (
        <PendingPayment
          order={order}
          verifying={verifying}
          cancelling={cancelling}
          onVerify={onVerify}
          onCancel={onCancel}
        />
      ) : (
        <OrderOutcome order={order} onRestart={onRestart} />
      )}
    </>
  );
}

// tone names the state without relying on its colour: paid is done, waiting is open, and the rest are closed.
function tone(state: Schemas["OrderState"]) {
  if (state === "paid") {
    return "ok" as const;
  }
  if (state === "pending" || state === "review") {
    return "warn" as const;
  }
  return "neutral" as const;
}

function PendingPayment({
  order,
  verifying,
  cancelling,
  onVerify,
  onCancel,
}: {
  order: Schemas["Order"];
  verifying: boolean;
  cancelling: boolean;
  onVerify: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      {order.checkout ? (
        <Text tone="muted">{t("topup.waiting")}</Text>
      ) : (
        <Stack gap={4}>
          <Text tone="muted">{t("topup.unconfirmed")}</Text>
          <Text tone="faint">{t("topup.unconfirmedBody")}</Text>
        </Stack>
      )}
      {order.checkout ? <Checkout checkout={order.checkout} /> : null}
      <Cluster>
        <Button variant="secondary" size="md" disabled={verifying} onClick={onVerify}>
          <Icon name="refresh" spin={verifying} />
          {t("topup.verify")}
        </Button>
        <Button variant="ghost" size="md" disabled={cancelling} onClick={onCancel}>
          {t("topup.cancel")}
        </Button>
      </Cluster>
      <Text size="sm" tone="faint">
        {t("topup.expires", { at: format.dateTime(new Date(order.expires_at)) })}
      </Text>
    </>
  );
}

// Checkout is where to pay: a link, a code to show, or a secret the channel's own page needs, each only when the
// channel gave it.
export function Checkout({ checkout }: { checkout: Schemas["Checkout"] }) {
  const { t } = useTranslation();
  return (
    <Stack gap={12}>
      {checkout.pay_url ? (
        <>
          <Button asChild variant="primary" size="md">
            <a href={checkout.pay_url} rel="noopener" referrerPolicy="origin">
              <Icon name="card" />
              {t("topup.payNow")}
            </a>
          </Button>
          <Text tone="faint">{t("topup.returnHint")}</Text>
        </>
      ) : null}
      {checkout.qr_code ? (
        <Stack gap={8}>
          <QrCode value={checkout.qr_code} label={t("topup.qr")} />
          <Copyable title={t("topup.qr")} hint={t("topup.qrHint")} code={checkout.qr_code} />
        </Stack>
      ) : null}
      {checkout.client_secret ? (
        <Copyable title={t("topup.secret")} hint={t("topup.secretHint")} code={checkout.client_secret} />
      ) : null}
    </Stack>
  );
}

function Copyable({ title, hint, code }: { title: string; hint: string; code: string }) {
  return (
    <Stack gap={8}>
      <Text tone="muted">{title}</Text>
      <Cluster>
        <CodeBlock size="sm">{code}</CodeBlock>
        <CopyButton text={code} />
      </Cluster>
      <Text size="sm" tone="faint">
        {hint}
      </Text>
    </Stack>
  );
}

function OrderOutcome({ order, onRestart }: { order: Schemas["Order"]; onRestart: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      {order.state === "paid" ? (
        <Text tone="muted">
          {t("topup.paidBody", { credits: format.money(order.credits) })}{" "}
          <TextLink>
            <Link to="/statement">{t("topup.seeStatement")}</Link>
          </TextLink>
        </Text>
      ) : null}
      {order.state === "review" ? <Text tone="muted">{t("topup.reviewBody")}</Text> : null}
      {order.state !== "paid" && order.state !== "review" ? (
        <Text tone="muted">{t("topup.closedBody", { state: t(`orders.state.${order.state}`) })}</Text>
      ) : null}
      <Cluster>
        <Button variant="secondary" size="md" onClick={onRestart}>
          <Icon name="plus" />
          {t("topup.new")}
        </Button>
        <Button asChild variant="ghost" size="md">
          <Link to="/orders">{t("topup.orders")}</Link>
        </Button>
      </Cluster>
    </>
  );
}
