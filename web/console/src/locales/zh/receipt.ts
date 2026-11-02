export default {
  title: "收据",
  back: "返回订单",
  print: "打印或存为 PDF",
  order: "订单号",
  paidAt: "支付时间",
  method: "支付方式",
  paid: "支付金额",
  credited: "入账额度",
  refunds: "退款",
  noRefunds: "没有退款。",
  refundLine: "退回 {{amount}}，扣回 {{credits}} 额度",
  footnote: "本收据由 {{site}} 依据订单记录生成；付款以支付渠道的记录为准。",
  none: {
    unpaid: {
      title: "还没有收据",
      body: "这笔订单还没有完成支付。支付完成后，收据会出现在这里。",
    },
    unavailable: {
      title: "暂时无法显示收据",
      body: "这笔订单的记录还不完整，暂时出具不了收据；稍后刷新再试。",
    },
  },
};
