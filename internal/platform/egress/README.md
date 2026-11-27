# egress

出站 HTTP 的唯一入口。出站的不变量见 [platform 的「不变量」](../README.md#不变量)，地址策略的推导在[存档](../../../docs/archive/platform.md)。

## 出站 HTTP

- `egress.New(egress.Direct)` 并发安全，`Via` 经 http(s)、socks5 代理；`DialContext` 给 SMTP 复用同一套校验；`ReadBody(body, max)` 超限报错、不返回截断内容。
- DNS 结果全部合格才拨号，直连按 IP 拨，Host 和 SNI 保留原主机；拒绝私网、回环、链路本地、特殊用途段、非 `2000::/3` 的 IPv6、NAT64 知名与本地前缀、Azure 的 `168.63.129.16`，以及 URL 里的映射 IPv6、非常规 IPv4 写法和 zone。
- 连接合计 10 秒（候选 IP 平分），TLS 握手 10 秒，空闲 90 秒，响应头至多 1 MiB；其余只受调用方期限约束。
- 部署前提：出站网络没有转向内网的自选 NAT64 前缀。
- 测试：`egresstest.Policy` 放行回环（含 mail 测试）；`Route(host, at)` 把固定公网域名转到本机假服务，Host 和证书不变，目标仍过策略。生产策略不转发。
