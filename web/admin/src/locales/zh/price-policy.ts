export default {
  title: "定价策略",
  empty: "没有可读取的定价策略",
  edit: "修改定价策略",
  save: "保存定价策略",
  intro: "策略影响售价计算及自带 Key 的收费。保存后，候选售价会重新计算。",
  invalid: "请填写三项比例。自带 Key 费率可留空，或填写 0 到 10,000 的整数。",
  ratioHint: "{{min}} 到 10 的比例，最多 15 位小数。",
  byokHint: "单位为基点，500 表示 5%。留空时保留当前值。",
  saved: "定价策略已保存。",
  fields: {
    floor_multiplier: "底价倍率",
    change_ratio: "变动比例",
    max_increase_ratio: "最大涨幅",
    byok_fee_bps: "自带 Key 费率",
  },
};
