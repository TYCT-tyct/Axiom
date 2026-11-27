// The words of confirming and clearing a base price (confirmSupplierBasePrices): for a model whose supplier states no
// price, a person's base, times the group's ratio, is its cost.
export default {
  basePrice: {
    open: "确认底价",
    openFor: "确认 {{name}} 的底价（分组 {{group}}）",
    title: "确认 {{name}} 的底价",
    body: "{{supplier}} 不公布这个模型的价格，所以成本未知、不进路由。底价按这个名字现在关联的目录模型 {{model}} 确认；对方以后公布价格时以对方为准。",
    formula:
      "成本 = 底价 ÷ 单位价值（向上取整）× 分组倍率（向上取整），下一轮同步后在端点的价目行显示。超出单价上限时成本仍未知，不会出现价目行。",
    source: "底价从哪里来",
    official: "目录参考价（{{source}}）",
    entered: "逐项填写",
    officialPrice: "目录里的参考价，来源：{{source}}",
    noOfficial: "目录里没有这个模型的 {{currency}} 参考价，请逐项填写。",
    enteredHint: "按 {{currency}} 填，没填的计费项不定价。",
    unitMissingBody: "底价要换算成渠道自己的余额单位，没有单位价值就存不下来。",
    unitAction: "先去确认单位价值",
    refused: {
      conflict: "没有保存：这个渠道还没确认单位价值、不是中转站，或者对方现在已经给这个模型报价，不再需要底价。",
      unprocessable: "这个底价存不下：名字还没关联到目录模型、价格全是 0，或者换算后超出单价上限。",
      stale_revision: "打开对话框后，这个名字改关联了别的目录模型，底价没有保存。请关掉重开，按现在的模型确认。",
    },
    done: "底价已确认，同步已提前到现在。下一轮之后打开这个端点看价目行，那里是实际计价用的成本。",
    clear: "取消底价",
    clearTitle: "取消 {{name}} 的底价",
    clearBody: "下一轮同步后，这个模型重新成本未知，端点不再进路由。",
    cleared: "底价已取消，下一轮同步后生效。",
    clearedNone: "这个模型没有确认过的底价，什么也没改。",
    waiting: "等你确认底价",
    waitingLede: "这些模型已经上架，但对方不公布价格，确认底价后才会有端点。",
    waitingNone: "没有等底价的模型。",
    waitingSub: "{{model}}，分组 {{group}}",
  },
};
