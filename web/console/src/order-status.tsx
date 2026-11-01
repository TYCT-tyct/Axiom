import type { Schemas } from "@axiom/api";
import { QrCode } from "@axiom/app";
import { Badge, Button, CopyButton, Icon, useFormat } from "@axiom/ui";
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
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Badge tone={tone(order.state)} icon={order.state === "paid" ? "check" : "refresh"}>
          {t(`orders.state.${order.state}`)}
        </Badge>
        <span className="text-ui text-t2">
          {t("topup.summary", { credits: format.money(order.credits), total: format.pay(order.total) })}
        </span>
      </div>
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
        <p className="max-w-prose text-ui text-t2">{t("topup.waiting")}</p>
      ) : (
        <div className="max-w-prose space-y-1">
          <p className="text-ui text-t2">{t("topup.unconfirmed")}</p>
          <p className="text-ui text-t3">{t("topup.unconfirmedBody")}</p>
        </div>
      )}
      {order.checkout ? <Checkout checkout={order.checkout} /> : null}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="md" disabled={verifying} onClick={onVerify}>
          <Icon name="refresh" className={verifying ? "animate-spin" : undefined} />
          {t("topup.verify")}
        </Button>
        <Button variant="ghost" size="md" disabled={cancelling} onClick={onCancel}>
          {t("topup.cancel")}
        </Button>
      </div>
      <p className="text-label text-t3">{t("topup.expires", { at: format.dateTime(new Date(order.expires_at)) })}</p>
    </>
  );
}

// Checkout is where to pay: a link, a code to show, or a secret the channel's own page needs, each only when the
// channel gave it.
export function Checkout({ checkout }: { checkout: Schemas["Checkout"] }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      {checkout.pay_url ? (
        <>
          <Button asChild variant="primary" size="md">
            <a href={checkout.pay_url} rel="noopener" referrerPolicy="origin">
              <Icon name="card" />
              {t("topup.payNow")}
            </a>
          </Button>
          <p className="text-ui text-t3">{t("topup.returnHint")}</p>
        </>
      ) : null}
      {checkout.qr_code ? (
        <div className="space-y-2">
          <QrCode value={checkout.qr_code} label={t("topup.qr")} />
          <CodeBlock title={t("topup.qr")} hint={t("topup.qrHint")} code={checkout.qr_code} />
        </div>
      ) : null}
      {checkout.client_secret ? (
        <CodeBlock title={t("topup.secret")} hint={t("topup.secretHint")} code={checkout.client_secret} />
      ) : null}
    </div>
  );
}

function CodeBlock({ title, hint, code }: { title: string; hint: string; code: string }) {
  return (
    <div className="space-y-2">
      <p className="text-ui text-t2">{title}</p>
      <div className="flex flex-wrap items-start gap-2">
        <code className="max-w-full rounded-btn bg-s2 px-3 py-2 font-mono text-label break-all text-t1">{code}</code>
        <CopyButton text={code} />
      </div>
      <p className="text-label text-t3">{hint}</p>
    </div>
  );
}

function OrderOutcome({ order, onRestart }: { order: Schemas["Order"]; onRestart: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      {order.state === "paid" ? (
        <p className="text-ui text-t2">
          {t("topup.paidBody", { credits: format.money(order.credits) })}{" "}
          <Link to="/statement" className="text-accent underline-offset-4 hover:underline">
            {t("topup.seeStatement")}
          </Link>
        </p>
      ) : null}
      {order.state === "review" ? <p className="text-ui text-t2">{t("topup.reviewBody")}</p> : null}
      {order.state !== "paid" && order.state !== "review" ? (
        <p className="text-ui text-t2">{t("topup.closedBody", { state: t(`orders.state.${order.state}`) })}</p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="md" onClick={onRestart}>
          <Icon name="plus" />
          {t("topup.new")}
        </Button>
        <Button asChild variant="ghost" size="md">
          <Link to="/orders">{t("topup.orders")}</Link>
        </Button>
      </div>
    </>
  );
}
