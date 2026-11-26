import alertConfig from "./alert-configuration";
import alertEvents from "./alert-events";
import announcements from "./announcements";
import approvals from "./approvals";
import audit from "./audit";
import calls from "./calls";
import exchangeRates from "./exchange-rates";
import legalDocs from "./legal-documents";
import mappings from "./mappings";
import models from "./models";
import operations from "./operations";
import orders from "./orders";
import discrepancies from "./payment-discrepancies";
import platformGrants from "./platform-grants";
import pricePolicy from "./price-policy";
import pricing from "./pricing";
import rateLimits from "./rate-limits";
import redeem from "./redeem";
import logRetention from "./retention";
import routingPresets from "./routing-presets";
import settings from "./settings";
import shell from "./shell";
import sourceClasses from "./source-classes";
import subscriptions from "./subscriptions";
import suppliers from "./suppliers";
import users from "./users";

// The admin's messages in Chinese, the source every other language follows.
export const initial = {
  brandName: "Axiom 管理后台",
  forbiddenHint: "请联系站长授予权限。",
  nav: {
    operationsCredentials: "运营凭证",
    operationsLimits: "资金与执行上限",
    users: "用户",
    orders: "订单与退款",
    discrepancies: "支付差异",
    redeemCodes: "兑换码",
    calls: "调用记录",
    audit: "审计日志",
    suppliers: "供应商",
    mappings: "待映射模型",
    models: "模型目录",
    pricing: "定价",
    exchangeRates: "汇率",
    subscriptionPlans: "订阅套餐",
    settings: "站点设置",
    sourceClasses: "来源类别",
    routingPresets: "路由预设",
    alertEvents: "告警事件",
    alertConfig: "告警规则与渠道",
    announcements: "公告",
    legalDocs: "法律文档",
    retention: "记录保留",
    rateLimits: "请求限流",
  },
  shell,
  calls,
  audit,
  sourceClasses,
  routingPresets,
  signIn: { description: "管理后台只对运营人员开放，登录后还要完成两步验证。" },
  overview: {
    targets: {
      month: "目标月份",
      monthHint: "按 UTC 自然月选择；页面上的时间按你的时区（{{zone}}）显示。",
      edit: "设置目标",
      title: "{{month}}的目标",
      intro:
        "设置这个月的目标。留空的项会清除该目标；仪表盘的「本月目标」把收入、付费账户、毛利率和留存率与这个月到现在的实际数字对照。",
      calls: "调用量",
      callsHint: "这个月至少要达到的调用量。",
      errors: "错误数",
      errorsHint: "这个月最多允许的错误数。",
      revenue: "收入（{{currency}}）",
      revenueHint: "这个月至少要达到的收入。",
      cost: "供应商成本（{{currency}}）",
      costHint: "这个月最多允许的供应商成本。",
      paidUsers: "付费用户",
      paidUsersHint: "这个月至少要有这么多账户付费充值。",
      monthlyRevenue: "月流水（{{currency}}）",
      monthlyRevenueHint: "这个月付费充值至少带来这么多额度；不是调用收入。",
      grossMargin: "毛利率（%）",
      grossMarginHint: "占调用收入的百分比，范围 0 到 100。",
      retention: "留存率（%）",
      retentionHint: "上月付费账户中本月再次付费的比例。",
      countInvalid: "请填写整数；留空表示清除目标。",
      moneyInvalid: "请填写金额，例如 1200.50，最多 38 位数字；留空表示清除目标。",
      rateInvalid: "请填写 0 到 100 之间的百分比，最多两位小数；留空表示清除目标。",
      save: "保存目标",
      saved: "这个月的目标已保存。",
    },
  },
  filter: { apply: "筛选", reset: "清除" },
  change: {
    reason: "理由",
    reasonHint: "为什么做这件事。会写进审计日志，之后可以查到。",
    reasonMissing: "请填写理由。",
    reasonPlaceholder: "写给以后查操作日志的人看",
    picksLabel: "常用理由",
    picks: { routine: "例行维护", request: "用户申请", incident: "故障处理", price: "价格调整", risk: "风险处理" },
    replayedToast: "已经生效，没有重复执行",
    undo: "撤销",
    undoReason: "撤销：{{reason}}",
    undone: "已撤销",
    undoRefused: "没有撤销：{{message}}",
    undoUnknown: "不确定撤销有没有生效。先看一下页面上的结果，不要再撤销一次。",
    confirmLabel: "输入“{{name}}”以确认",
    confirmMismatch: "输入的名称和要操作的对象不一致。",
    done: "已完成。",
    replayed: "这次操作之前已经完成过，下面是当时的结果，没有重复执行。",
    close: "关闭",
    unknown:
      "不确定这次改动有没有生效。内容已锁定：重发会原样发出同一个请求，服务端最多执行一次，这是弄清结果最可靠的办法。关闭后这个请求会保留：再次打开这项改动时，可以原样重发或放弃。页面上的数据不能证明它没有生效。",
    unsettled:
      "你之前在这里做的一次改动没有收到回应，不确定有没有生效。可以原样重发（服务端最多执行一次，已经执行过会告诉你），或放弃它。放弃后它仍可能已经生效：重新做之前先核对数据，因为页面上的数据不能证明它没有生效。",
    giveUp: "放弃",
    resend: "原样重发",
    stale:
      "打开之后，这一项被别人改过。为了不覆盖那次改动，这次没有保存。重新载入会换成最新的内容，你在这里填写的会被丢弃。",
    reload: "重新载入",
    reused:
      "这个请求的标识已经用于另一份内容，第一次提交可能已经生效，这里不会再发送。关闭后看到的数据可能还不包含它，不能据此判断要不要重做。",
  },
  // The words of the settings in the registry (settings-registry.ts), here and not with their page, so the palette finds
  // them before any settings page is opened.
  setting: {
    registration: {
      mode: "谁可以注册",
      modeDetail: "关掉之后，已有的账号不受影响。开放注册需要能发信的节点。",
      humanCheck: "注册时做人机验证",
      humanCheckDetail: "浏览器先解一道和本站、邮箱绑定的小题，服务器才保存注册请求，拦住批量注册。",
      bits: "验证难度",
      bitsDetail: "12 到 24 位，越高浏览器算得越久。",
      burst: "同一个邮箱域名，一次最多注册",
      burstDetail: "1 到 10,000。",
      perHour: "同一个邮箱域名，每小时最多注册",
      perHourDetail: "1 到 100,000。",
      allow: "只允许这些邮箱域名",
      allowDetail: "留空表示不限制。一项包含它的子域名。",
      deny: "不允许的邮箱域名",
      denyDetail: "和允许的冲突时，以这里为准。常见的一次性邮箱可以加在这里。",
    },
  },
  // A settings page's changes that are not yet submitted (settings-drafts.tsx).
  drafts: {
    pending_one: "{{count}} 项修改还没提交",
    pending_other: "{{count}} 项修改还没提交",
    invalid_one: "{{count}} 项填得不对，改好才能提交",
    invalid_other: "{{count}} 项填得不对，改好才能提交",
    discard: "放弃",
    discarded: "已放弃修改",
    review: "查看并提交",
    title_one: "提交 {{count}} 项修改",
    title_other: "提交 {{count}} 项修改",
    intro: "提交后马上生效。几组设置分开保存时，按顺序逐组提交；没提交成功的那组留在页面上，可以改了再提交。",
    action: "提交",
    submitted_one: "已提交 {{count}} 项修改",
    submitted_other: "已提交 {{count}} 项修改",
    done: "已提交。",
    went: "已提交：{{names}}",
    failed: "没有提交：{{names}}。{{message}}",
    waiting: "还没发出：{{names}}",
    moved: "{{names}} 在你改了之后被别人改过。为了不覆盖那次修改，先还原这几项，看过最新的再改。",
    revert: "还原",
    revertName: "还原「{{name}}」",
    on: "开",
    off: "关",
    none: "无",
    remove: "去掉 {{value}}",
    leaveTitle: "还有修改没提交",
    leaveBody: "离开这一页，没提交的修改会丢掉。",
    leave: "离开",
    stay: "留下",
  },
  users,
  orders,
  mappings,
  models,
  pricing,
  redeem,
  approvals,
};

// The complete catalog is for types and tooling; the app imports only initial.
export default /* @__PURE__ */ Object.assign({}, initial, {
  operations,
  settings,
  subscriptions,
  suppliers,
  discrepancies,
  exchangeRates,
  platformGrants,
  pricePolicy,
  alertConfig,
  alertEvents,
  announcements,
  legalDocs,
  rateLimits,
  logRetention,
});
