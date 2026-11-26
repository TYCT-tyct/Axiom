export default {
  description: "你自己的登录：这次登录什么时候会结束，两步验证和恢复码。",
  account: { title: "账号", id: "账户编号" },
  session: {
    title: "这次登录",
    detail: "不操作的话，到 {{at}} 会退出。",
    signOut: "退出登录",
  },
  twoStep: {
    title: "两步验证",
    lede: "进管理后台每次都要验证，不能关闭。",
    verifier: "验证器",
    verifierDetail: "登录时用它生成的 6 位数字验证。",
    bound: "已绑定",
    unbound: "没有绑定",
    codes: "恢复码",
    codesDetail: "手机不在身边时用。每个只能用一次；重新生成后，现在的恢复码马上全部作废。",
    regenerate: "重新生成",
  },
};
