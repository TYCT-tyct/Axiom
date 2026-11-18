// The statement page's messages. They are the namespace whose new labels pushed the English root locale file past the
// code-line limit in scripts/limits.ts, so they live in their own module; both languages stay together because a label
// and its translation are edited as a pair.
import type { Messages } from "@axiom/ui";
import { en as billEn, zh as billZh } from "./bill";

export const zh = {
  bill: billZh,
  description: "账户额度的充值、赠送、扣费、到期、退款冻结与已完成退款。",
  movements: "流水",
  export: "导出 CSV",
  exportFailed: "导出没有成功：{{reason}}",
  note: "按入账顺序从新到旧排列。冻结额仍包含在余额内，退款成功才扣余额；冻结额不含在途调用预留。导出含最近的 10 000 条，更早的在页面上翻页查看。",
  segment: { title: "这一段没有余额变动", body: "加载更多，继续查看更早的流水。" },
  empty: {
    title: "还没有额度变动",
    body: "充值、赠送或调用扣费之后，记录会出现在这里。",
    balance: "查看余额",
  },
  col: { time: "时间", kind: "类型", amount: "金额", balance: "变动后余额" },
  kind: {
    top_up: "充值",
    gift: "赠送",
    redeem: "兑换码",
    spend: "调用扣费",
    expiry: "到期",
    refund: "退款完成",
    reversal: "调用退费",
    freeze: "退款冻结",
    unfreeze: "退款解冻",
    chargeback: "拒付",
    chargeback_reversal: "拒付冲正",
  },
  frozenChange: "冻结额变动",
  frozenAfter: "冻结：{{amount}}",
  balanceUnknown: "不可用",
  balanceUnknownNote: "这笔记录早于账本编号，先后顺序无法确定，所以无法给出变动后余额。",
  call: "调用 {{id}}",
};

export const en: Messages<typeof zh> = {
  bill: billEn,
  description: "Top-ups, gifts, charges, expiries, refund holds and completed refunds.",
  movements: "Movements",
  export: "Export CSV",
  exportFailed: "The export failed: {{reason}}",
  note: "Newest first in posting order. Held credits remain in the balance until refunded; holds do not include pending call reservations. The export holds the newest 10,000 rows; page here for the rest.",
  segment: {
    title: "No balance changes in this segment",
    body: "Load older events to continue through your statement.",
  },
  empty: {
    title: "No movements yet",
    body: "Top-ups, gifts and charges show up here.",
    balance: "See your balance",
  },
  col: { time: "Time", kind: "Kind", amount: "Amount", balance: "Balance after" },
  kind: {
    top_up: "Top-up",
    gift: "Gift",
    redeem: "Redeem code",
    spend: "Call charge",
    expiry: "Expired",
    refund: "Refund paid",
    reversal: "Call refund",
    freeze: "Refund hold",
    unfreeze: "Hold released",
    chargeback: "Chargeback",
    chargeback_reversal: "Chargeback reversed",
  },
  frozenChange: "Change in held credits",
  frozenAfter: "Held: {{amount}}",
  balanceUnknown: "Not available",
  balanceUnknownNote: "Recorded before posting order was kept, so the balance after it is unknown.",
  call: "Call {{id}}",
};
