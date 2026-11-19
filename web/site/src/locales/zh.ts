import first from "@axiom/boards/Site/regions/FirstScreen.zh";
import headline from "@axiom/boards/Site/regions/Headline.zh";
import wide from "@axiom/boards/Site/regions/Shell.zh";
import narrow from "@axiom/boards/SitePhone/regions/Shell.zh";

// The website's messages in Chinese, the source every other language follows.
export default {
  home: "{{site}} 首页",
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
    noApi: "本站 API 地址暂不可用",
    connect: {
      terminal: "终端",
      cursorSettings: "Cursor 设置 → Models",
      alsoZshrc: "# 也可以写进 ~/.zshrc",
      setKey: "# 在终端设置key；model 填这把key的模型列表里的名字",
      pickModel: "# 选好模型，照常使用",
      compatOpenai: "# 兼容 OpenAI 接口",
      compatAnthropic: "# 兼容 Anthropic 接口",
      hello: "你好",
      yourKey: "你的key",
      other: "其他工具",
      copy: "复制配置",
      modelList: "# model 填这把key的模型列表（GET /v1/models）里的名字",
      copyUnavailable: "无法复制：本站 API 地址暂不可用",
      // shown where the file's name is, so it is short
      copyFailed: "复制失败，请手动选择",
      copied: "已复制",
      // the heading's comma (the board's kern), its promise, which holds for a key that may use only some models, and the
      // lede, which names the site
      comma: "，",
      every: "Key允许的模型都能用",
      lede: "把工具的接口地址换成{{site}}的，填入key。命令、代码和工作流都不用改。",
    },
    // Each answer holds for every site's settings: what is billed (docs/domains/billing.md「计费项」「预留与结算」),
    // when a call moves to another endpoint (docs/domains/routing.md, provider.allow_fallbacks), which models a key
    // reaches (docs/domains/accounts.md「API key」) and how the site takes new users (getSite's registration).
    faq: {
      a0: "一个Key就能调用多家厂商的模型（能调哪些，由这把Key的设置决定），不用每家各开账号、各自充值。同一个模型背后可以有多个端点，{{site}}默认按价格和健康度挑一个；允许回退时，一个失败了就换下一个可用的。账单只有一份。",
      a1: "不是模型厂商自己提供的端点，通常更便宜。要限制来源，在控制台建一个路由预设（比如只走官方来源），再把它绑到这把Key上；只改默认预设不会改已有的Key。",
      a2: "Claude Code、Codex、Cursor和OpenAI SDK都能用，改地址和Key，再从这把Key的模型列表里选模型。其他兼容OpenAI或Anthropic接口的工具也一样。",
      a3: "按实际服务这次请求的那个端点收费。每个端点有自己的计费项和单价：对话一般按输入、输出token，图片按张，联网搜索这类工具按次。价格公开的站点，模型页会列出各端点的价格；不公开的站点，登录后才看得到。",
      a4: "允许回退、还有可用的端点时，{{site}}会换一个端点重试；内容开始交给你之后就不再换。换端点之前失败的尝试不向你收费；最后一次按它的结局计费：内容已经交到你手里的，按实际用量收；已经执行的按次项目（比如搜索）照收。控制台的调用记录里能看到每次尝试。",
      a5: "端点价格会跟着上游调整。每个请求按它发出时锁定的价格结算，之后的调价不影响已经发出的请求；价格可见的站点，模型页显示的是当前价格。",
      a6: "在控制台删掉这个Key，再新建一个。平时给每个Key设好额度上限和IP白名单，就算泄露，损失也有限。",
      // by how the site takes new users
      a7: {
        open: "注册不要钱。目前没有试用额度，按用量扣费，用多少扣多少。",
        invite: "注册不要钱，不过本站要凭邀请码注册。目前没有试用额度，按用量扣费，用多少扣多少。",
        closed: "本站暂不开放注册，已有账户直接登录。没有试用额度，按用量扣费，用多少扣多少。",
      },
      // the last picture's billing line: not every endpoint bills by the token
      billing: "按用量",
      // after the answers whose picture shows figures: they are the board's illustration, not the site's
      sample: "图中的价格、金额和 Key 是示例数据。",
    },
    // The sea bed's way in and the line before it, by how the site takes new users (getSite's registration): a site that
    // takes nobody new is entered by signing in. The brand's line holds for a key that may use only some models.
    bed: {
      tagline: "一个Key，接入它能用的所有模型。",
      join: {
        open: "注册，获取API key",
        invite: "凭邀请码注册，获取API key",
        closed: "登录，获取API key",
      },
      lede: {
        open: "注册就能拿到Key，改两行配置，接着用你现在的工具。",
        invite: "凭邀请码注册就能拿到Key，改两行配置，接着用你现在的工具。",
        closed: "登录控制台建一把Key，改两行配置，接着用你现在的工具。",
      },
    },
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
      listLabel: "模型",
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
