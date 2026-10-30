import type { AdminSchemas } from "@axiom/api";
import { MerchantsCard } from "./merchants";
import { PaymentCard } from "./payment-card";

export function PaymentsSettings({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  return (
    <>
      <PaymentCard siteId={siteId} currency={currency} />
      <MerchantsCard siteId={siteId} currency={currency} />
    </>
  );
}
