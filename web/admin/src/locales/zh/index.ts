import audit from "./audit";
import calls from "./calls";
import mappings from "./mappings";
import models from "./models";
import orders from "./orders";
import pricing from "./pricing";
import redeem from "./redeem";
import settings from "./settings";
import suppliers from "./suppliers";
import users from "./users";

// The admin's messages in Chinese, the source every other language follows.
export default {
  brandName: "Axiom 管理后台",
  forbiddenHint: "请联系站长授予权限。",
  nav: {
    overview: "运营看板",
    users: "用户",
    orders: "订单与退款",
    redeemCodes: "兑换码",
    calls: "调用记录",
    audit: "审计日志",
    suppliers: "供应商",
    mappings: "待映射模型",
    models: "模型目录",
    pricing: "定价",
    settings: "站点设置",
  },
  calls,
  audit,
  signIn: { description: "管理后台只对运营人员开放，登录后还要完成两步验证。" },
  home: {
    title: "运营看板",
    description: "所选时段内本站的调用、收入、成本与现金；每个数字都能下钻到对应的调用记录、订单或现金流水。",
    noAccessTitle: "还没有可用的页面",
    noAccessBody: "你的账户没有任何管理权限。请联系负责人授权。",
  },
  overview: {
    rangeLabel: "时间范围",
    range: { "24h": "24 小时", "7d": "7 天", "30d": "30 天" },
    window: "{{since}} 至 {{until}}",
    calls: "调用量",
    errors: "错误数",
    revenue: "收入",
    revenueNote: "用户实付，已扣除平台承担的部分",
    cost: "供应商成本",
    costNote: "已按汇率换算的成本",
    unknownCost: "{{calls}} 次调用的成本未知，未计入合计",
    callsIncomplete: "{{since}} 之前的调用已归档，调用量和排名不含它们；收入、成本和现金仍覆盖整个时段。",
    callsIncompleteUnknown:
      "此节点保留的调用记录之前的调用已归档，调用量和排名不完整；收入、成本和现金仍覆盖整个时段。",
    coverageUnavailable: "此节点无法判断较早的调用是否已归档，调用量和排名可能不完整。",
    cash: "现金净流入",
    cashNote: "充值实付减退款，不是调用收入",
    unavailable: "此节点不提供",
    topAccounts: "调用最多的账户",
    topModels: "调用最多的模型",
    col: { account: "账户", model: "模型", calls: "调用", errors: "错误" },
    noAccounts: { title: "该时段没有账户调用", body: "扩大时间范围试试。" },
    noModels: { title: "该时段没有模型被调用", body: "扩大时间范围试试。" },
  },
  filter: { apply: "筛选", reset: "清除" },
  change: {
    reason: "理由",
    reasonHint: "为什么做这件事。会写进审计日志，之后可以查到。",
    reasonMissing: "请填写理由。",
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
  users,
  orders,
  suppliers,
  mappings,
  models,
  pricing,
  redeem,
  settings,
};
