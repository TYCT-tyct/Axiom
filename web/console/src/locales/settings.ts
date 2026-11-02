// The settings page's messages. They live in their own module for the same reason as the receipt namespace: the English
// root locale file is at the code-line limit in scripts/limits.ts. A label and its translation are edited as a pair, so
// both languages stay together.
import type { Messages } from "@axiom/ui";

export const zh = {
  description: "选择调用怎么付费。",
  billing: {
    title: "扣费顺序",
    preference: "付费方式",
    hint: "站点默认是「{{fallback}}」。",
    site: "跟随站点默认（{{value}}）",
    values: {
      subscription_first: "先套餐，后钱包",
      wallet_first: "先钱包，后套餐",
      subscription_only: "只扣套餐",
      wallet_only: "只扣钱包",
    },
    fallback:
      "一次调用只用一个来源。套餐优先的顺序在套餐当前窗口足够时扣套餐；钱包优先的顺序扣余额。两个「只扣」的顺序在来源不够时直接拒绝，不会改用另一个来源。",
  },
  save: "保存",
  saved: "已保存。",
  dismiss: "关闭",
};

export const en: Messages<typeof zh> = {
  description: "Choose how calls are paid for.",
  billing: {
    title: "Funding order",
    preference: "Pay with",
    hint: "The site's default is {{fallback}}.",
    site: "Use the site default ({{value}})",
    values: {
      subscription_first: "Subscription first, then the wallet",
      wallet_first: "Wallet first, then the subscription",
      subscription_only: "Subscription only",
      wallet_only: "Wallet only",
    },
    fallback:
      'A request uses one source. The subscription orders take the plan when its current window covers the call; the wallet orders take the balance. The two "only" orders refuse the call instead of using the other source.',
  },
  save: "Save",
  saved: "Saved.",
  dismiss: "Close",
};
