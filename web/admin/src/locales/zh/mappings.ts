export default {
  description: "供应商列出、但目录认不出的模型名。映射之前它们没有端点，也不接收流量；最早出现的排在最前。",
  empty: { title: "没有待映射的模型", body: "供应商列出的模型名目录都认得。" },
  anySupplier: "全部供应商",
  col: { name: "上游的模型名", supplier: "供应商", formats: "支持的格式" },
  map: "映射",
  dialog: {
    title: "映射模型名",
    body: "这个名字是哪个模型？上游的名字：",
    action: "映射",
    model: "对应的模型",
    scope: "作用范围",
    scopeGroup: "只在这个分组（{{group}}）",
    scopeSupplier: "整个供应商",
    modelMissing: "请从建议里选一个目录里已有的模型。",
    done: "已映射“{{name}}”。",
    wait: "它生成的端点要等真伪检测通过才接收流量。映射不会被改动，映射错了用更窄范围的新映射纠正。",
  },
};
