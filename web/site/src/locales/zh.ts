import first from "@axiom/boards/Site/regions/FirstScreen.zh";
import headline from "@axiom/boards/Site/regions/Headline.zh";
import wide from "@axiom/boards/Site/regions/Shell.zh";
import narrow from "@axiom/boards/SitePhone/regions/Shell.zh";

// The website's messages in Chinese, the source every other language follows.
export default {
  home: "首页",
  navigation: "主导航",
  models: "模型与价格",
  signIn: "登录",
  // the home page's words, by the keys the board's regions give them (packages/boards/manifest/site.ts)
  board: { ...wide, ...narrow, ...first, ...headline },
  // the language as its own switch names it
  language: "简体中文",
  // the sections of the home page from the top, as its island names them
  sections: {
    surface: "海面",
    connect: "接入",
    models: "模型与线路",
    steps: "三步开始",
    production: "上生产前",
    faq: "常见问题",
    seabed: "海底",
  },
  theme: {
    toDark: "切换到深色",
    toLight: "切换到浅色",
  },
  // the words the screens' scripts write (home/*-script.ts), and the models screen's states
  script: {
    models: {
      routes_one: "{{count}} 条线路",
      routes_other: "{{count}} 条线路",
      timeout: "超时",
      current: "当前选择",
      switched: "会改走",
      cheapest: "价格最低",
      // the failover is the screen's demonstration, not an event of the site's routes
      failed: "演示：假如线路 {{route}} 超时，请求会自动重试",
      say: "{{model}}，当前选择线路 {{route}}",
      unit: "价格单位：{{unit}}，每百万 token",
      noPrice: "本站的价格登录后可见",
      // the board's week of tokens, which the API does not publish
      sampleTokens: "42.1 亿",
      sample: "示例数据",
      unavailable: "不可用",
      noneInFilter: "这个开发方没有上架的模型",
      emptyTitle: "还没有上架的模型",
      emptyBody: "上架之后，模型和它们的线路会显示在这里。",
    },
    steps: {
      // after the screen's lede: what the demonstration shows is the board's, not an account's
      sample: "演示中的 Key、耗时和金额是示例数据。",
      noKey: "还没有Key",
      creating: "创建中",
      created: "已创建",
      copy: "复制",
      copied: "已复制",
      justNow: "刚刚",
      editing: "编辑中",
      saved: "已保存",
      noApi: "本站 API 地址暂不可用",
      waitSend: "等待发送",
      waitReply: "等待响应",
      ok: "OK",
    },
    trust: {
      sample: "演示中的价格、账单和额度是示例数据。",
      // who is not charged is the billing rule's (docs/domains/billing.md「免单规则」)
      ledgerNote:
        "模型、线路、token、金额一条一条记下，能按Key筛选，能导出CSV。没有内容交到你手里就失败的请求，不收模型用量。",
      inUse: "使用中",
      timeout: "超时",
      free: "不收费",
      calling: "正常调用中",
      stopped: "额度用完，已停止调用",
    },
  },
  footer: "© Axiom · 本站中文使用了 MiSans 字体",
};
