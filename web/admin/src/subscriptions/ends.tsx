import type { AdminSchemas } from "@axiom/api";
import { Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";

type Subscription = AdminSchemas["Subscription"];

const day = 24 * 60 * 60 * 1000;

// Whole days from now to when it ends, counted up: a subscription that ends in 30 hours has 2 days left.
export const daysLeft = (endsAt: string, now: number) => Math.ceil((Date.parse(endsAt) - now) / day);

// A share of the window's quota used, 0 to 1, from the exact amounts, which are integers in billionths.
export const usedShare = (sub: Subscription) => {
  const quota = BigInt(sub.current_window.quota);
  return quota === 0n ? 0 : Number((BigInt(sub.current_window.used) * 10_000n) / quota) / 10_000;
};

// When a subscription ends, and for an active one how long is left; within 7 days the words are the warning colour.
export function Ends({ sub, now }: { sub: Subscription; now: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const left = daysLeft(sub.ends_at, now);
  const running = sub.state === "active";
  return (
    <>
      <Text as="div" numeric>
        {format.dayFull(new Date(sub.ends_at))}
      </Text>
      {running ? (
        <Text as="div" size="sm" tone={left <= 7 ? "warn" : "faint"}>
          {left <= 0 ? t("subscriptions.today") : t("subscriptions.daysLeft", { count: left })}
        </Text>
      ) : null}
    </>
  );
}
