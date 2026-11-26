// The words of each settings page: its lede and the titles of its cards (adPg_* in admin_set.js).
export default {
  site: {
    lede: "站点的名字、标志和品牌色，以及价格是否公开、先用哪种额度扣费。",
    brand: "品牌",
    brandLede: "用户在登录页、控制台和邮件里看到的样子",
    billing: "计费和展示",
    about: "关于",
    id: "站点编号",
    idDetail: "联系支持或接入运营 agent 时会用到",
    copyId: "复制站点编号",
  },
  signIn: {
    lede: "用户可以用什么登录。",
    domain: "域名和通行密钥",
    providers: "第三方登录",
    providersLede: "用户可以用这些账号直接登录，第一次登录时自动建账号",
  },
  registration: {
    lede: "谁可以注册，以及怎样拦住批量注册。",
    entry: "注册入口",
    domains: "邮箱域名",
    domainsLede: "限制同一个域名的批量注册，或只允许指定域名的邮箱",
  },
  sessions: {
    lede: "登录保持多久，以及尝试登录太多次怎么办。",
    keep: "登录保持多久",
    attempts: "登录尝试限制",
    attemptsLede: "同一个来源尝试太多次就暂时拒绝",
  },
  limits: {
    lede: "这里是所有账户的默认上限。个别账户要放宽，到「用户，账户」里单独改。",
    keys: "Key",
    workspaces: "工作区",
  },
  payments: {
    lede: "用户能否充值、单次充值的范围，以及收钱的商户账户。",
    top: "充值",
  },
  mail: {
    lede: "邮件从哪台服务器发出。",
    service: "发信服务",
    serviceLede: "验证码、余额提醒、告警和订阅到期通知都从这里发",
  },
  gateway: {
    lede: "额度预留、质量检测和使用记录保留的数字。",
    reserve: "额度预留",
    reserveLede: "请求还没结束时，先冻结一部分额度，防止余额被用穿",
    probes: "质量检测",
    probesLede: "定期用真实请求检查每个端点，确认模型没有被换、没有被降级",
    log: "使用记录",
  },
  agent: {
    lede: "自动做日常巡检、客服补偿和调价建议的程序。这里设定它的权限上限。",
    approvals: "查看审批",
    execution: "执行上限",
    range: "调价范围",
    rangeLede: "agent 提出的售价变更必须落在这个范围里",
  },
};
