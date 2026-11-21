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
      // Codex's file holds no key: the key is set in a terminal, a line that stays a comment here, so the card is a valid file
      setKey: "# model 填这把key的模型列表里的名字；key 在终端里设置：",
      pickModel: "# 选好模型，照常使用",
      compatOpenai: "# 兼容 OpenAI 接口",
      compatAnthropic: "# 兼容 Anthropic 接口",
      hello: "你好",
      yourKey: "你的key",
      other: "其他工具",
      modelList: "# model 填这把key的模型列表（GET /v1/models）里的名字",
      // what each tool's copy button copies
      copyTerminal: "复制终端命令",
      copyFile: "复制配置文件",
      copyUrl: "复制 Base URL",
      copyCode: "复制代码",
      copyEnv: "复制 .env",
      copyUnavailable: "无法复制：本站 API 地址暂不可用",
      // the card without an address: no command to show
      noApiCard: "# 本站 API 地址暂不可用，暂时没有可用的配置",
      // shown where the file's name is, so it is short
      copyFailed: "复制失败，请手动选择",
      copied: "已复制",
      // the heading's comma (the board's kern), its promise, which holds for a key that may use only some models, and the
      // lede, which names the site
      comma: "，",
      every: "Key允许的模型都能用",
      lede: "把工具的接口地址换成{{site}}的，填入key，其余照常使用。",
    },
    // Each answer holds for every site's settings: what is billed and how (docs/domains/billing.md「定价」「用量来源与暂估」
    // 「免单规则」), when a call moves to another endpoint (docs/domains/routing.md「请求覆盖」「失败怎么办」), which sources
    // are official (docs/product.md「售卖形式」), which models a key reaches and its limits (docs/domains/accounts.md「API
    // key」) and how the site takes new users (getSite's registration). The PR's table lists each sentence's basis.
    faq: {
      a0: "一个Key就能调多家厂商的模型，不用每家各开账号、各自充值。同一个模型背后可以有多个端点，{{site}}按价格和健康度挑一个，端点出问题且允许回退时换下一个。账单只有一份。",
      a1: "我们直接接入的官方 API（含云厂商）之外的端点，多是中转站的分组，通常更便宜。要限制来源，在控制台建一个路由预设（比如只走官方来源），绑到这把Key上。",
      a2: "Claude Code、Codex、Cursor和OpenAI SDK都能用：设好地址和Key，再从这把Key的模型列表里选模型。其他兼容OpenAI或Anthropic接口的工具也一样。",
      a3: "按实际服务请求的端点收费：对话按输入、输出token，图片按张或token，搜索这类工具按次。上游没报全的用量按估算收；整笔都是估算的，对上供应商记录后会重算。",
      a4: "端点超时、限流或上游出错，且允许回退时，{{site}}会换一个端点重试。换之前失败的尝试不收费；请求本身的错误直接返回给你。控制台的调用记录里能看到每次尝试。",
      a5: "价格按本站的定价规则定，可能随上游成本或参考价调整；涨幅超过站点允许的范围时，模型先暂停新的调用，等站点确认。每个请求按它发出时锁定的价格结算。",
      a6: "在控制台删掉这个Key，再新建一个。平时给每个Key设好额度上限和IP白名单；上限拦的是新调用，已发出的请求仍可能让累计花费超过上限，所以发现泄露要尽快删。",
      // by how the site takes new users, and before the site has said: then only what holds for every site
      a7: {
        open: "注册不要钱。目前没有试用额度，按用量扣费，用多少扣多少。",
        invite: "注册不要钱，不过本站要凭邀请码注册。目前没有试用额度，按用量扣费，用多少扣多少。",
        closed: "本站暂不开放注册，已有账户直接登录。没有试用额度，按用量扣费，用多少扣多少。",
        unknown: "注册不要钱；本站是否开放注册、要不要邀请码，以注册页为准。没有试用额度，按用量扣费，用多少扣多少。",
      },
      // the last picture's lines that depend on the site: signing up, and billing, which is not by the token everywhere
      signUp: {
        open: "免费",
        invite: "凭邀请码，免费",
        closed: "暂不开放",
        unknown: "以注册页为准",
      },
      billing: "按用量",
      // the tools' picture: the two addresses, which are all it shows; without an address, no command
      envNote: "# 两种接口的地址；key 另外设置",
      // the label on the pictures that show prices, bills or keys: they are the board's illustration, not the site's
      sample: "示例数据",
    },
    // The sea bed's way in and the line before it, by how the site takes new users (getSite's registration): a site that
    // takes nobody new, or has not said yet, is entered by signing in. The brand's line holds for a key that may use only
    // some models.
    bed: {
      tagline: "一个Key，接入它能用的所有模型。",
      join: {
        open: "注册，获取API key",
        invite: "凭邀请码注册，获取API key",
        signIn: "登录，获取API key",
      },
      lede: {
        open: "注册就能拿到Key，把工具的地址和Key换成本站的，接着用你现在的工具。",
        invite: "凭邀请码注册就能拿到Key，把工具的地址和Key换成本站的，接着用你现在的工具。",
        signIn: "登录控制台建一把Key，把工具的地址和Key换成本站的，接着用你现在的工具。",
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
  // the sea's easter eggs (home/easter-eggs.tsx, home/terminal.ts): the sea-floor terminal opened with /, the way back up
  // from the floor, and the greeting in the browser's console
  eggs: {
    terminal: "海底终端",
    input: "输入命令",
    hello: "海底终端，输入 help 看看能做什么，Esc 关闭",
    help: {
      ls: "列出模型和线路",
      ping: "ping [模型]",
      pingDoes: "测一次到本站的往返",
      feed: "喂鱼",
      dive: "潜到海底",
      surface: "回到海面",
      whoami: "你是谁",
      clear: "清屏",
      whale: "叫鲸来",
    },
    ls: {
      more: "还有 {{count}} 个，完整列表见 /models",
      none: "还没有上架的模型",
      failed: "ls: 模型列表暂时取不到，稍后再试",
    },
    ping: {
      missing: "ping: 没有叫 {{name}} 的模型，试试 ls",
      listed: "{{model}}  已上架",
      // the public API publishes no latency: what ping measures is this browser's round trip to the site
      trip: "{{host}}  往返 {{ms}}ms  ok",
      failed: "ping: {{host}} 这次没有回应",
    },
    feed: "鱼来了。",
    dive: "下潜中 −1200m",
    surface: "上浮中 −0m",
    whale: "鲸来了。",
    whoami: "一位好奇的开发者。",
    // when the site takes anyone, before the sea bed tagline
    minute: "注册只要一分钟：{{tagline}}",
    sudo: "鲨鱼不听 sudo 的。",
    unknown: "{{command}}: 没有这个命令，试试 help",
    floor: "↑ 回到海面",
    console: "axiom.dive()  潜到海底    axiom.feed()  喂鱼    按 / 打开海底终端",
  },
  footer: "© Axiom · 本站中文使用了 MiSans 字体",
};
