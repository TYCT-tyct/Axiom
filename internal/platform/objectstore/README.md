# 对象存储

用 S3 接口读写对象。程序里只有这一份对象存储客户端；备份任务仍用自己的桶和凭证（`deploy/ops`），归档和对话正文等
功能落地时调用这里。对话正文不经过它：保存正文的平台开关仍然是关的（[记录与告警](../../../docs/domains/observability.md#保留与开关)）。

架构上的选型是「只依赖 S3 接口」，云厂商的对象存储和自托管的 SeaweedFS 都能接（[总体架构的技术选型](../../../docs/architecture.md#技术选型)）。本包不引入
AWS SDK：用到的只是签名、五个对象操作和一次列举，自己实现 SigV4 少一批依赖；签名与 S3 的互操作由真实的 SeaweedFS
容器测试守住。

## 接口

```go
type Config struct { Endpoint, Bucket, Region, AccessKeyID string; SecretAccessKey secret.Secret; PathStyle bool }
func ReadConfig(env *config.Env) (Config, error)   // 一个变量都没设时返回零值，表示关闭
func Open(cfg Config) (*Client, error)

func (c *Client) Check(ctx) error                  // HEAD 桶：核对桶存在、凭证可用，不读不写
func (c *Client) Put(ctx, Upload) (ObjectInfo, error)
func (c *Client) Get(ctx, key) (*Object, error)    // 调用方关闭 Body
func (c *Client) Head(ctx, key) (ObjectInfo, error)
func (c *Client) Delete(ctx, key) error            // 已经不存在的键也算成功
func (c *Client) List(ctx, ListOptions) (ListPage, error)
```

`List` 一次返回一页和下一页的令牌；要整个前缀的调用方顺着 `NextToken` 循环。`Upload.Size` 在调用方知道字节数时给出
（文件 `Stat` 一般知道），`-1` 表示由客户端边算哈希边数；给了但对不上是错误，不发请求。

## 配置

变量在仓库根的 `.env.example` 和 `deploy/.env.example` 里说明，部署时由 `deploy/compose.yaml` 传进 `axiom` 服务：

| 变量 | 含义 |
|---|---|
| `AXIOM_OBJECTSTORE_ENDPOINT` | S3 兼容端点，绝对 http(s) 地址，可带网关的挂载路径；不带结尾斜杠 |
| `AXIOM_OBJECTSTORE_BUCKET` | 桶名 |
| `AXIOM_OBJECTSTORE_REGION` | 签名凭证范围里的区域，默认 `us-east-1` |
| `AXIOM_OBJECTSTORE_ACCESS_KEY_ID` | 访问键 |
| `AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY` | 密钥；从不出现在错误、日志或 `%v` 里 |
| `AXIOM_OBJECTSTORE_PATH_STYLE` | `true`（默认）把桶放在路径里，SeaweedFS、R2、MinIO 要这样；`false` 放在主机名里，AWS S3 这样 |

六个变量全不设是「关闭」：`ReadConfig` 返回零值，`Open` 返回 `ErrNotConfigured`。设了任何一个就要设全，否则启动
失败并一次列出缺的变量名；错误只报变量名和要求的形状，不报值，密钥赋错变量时不会进日志。

`serve` 启动时读取并校验这组变量，所以部署设了它就能正常启动；现在还没有程序内的功能写这个桶，`axiom objectstore`
是第一个用它的调用方。

## 期限与重试

- **每次操作都要期限**：`ctx` 上没有 deadline 时返回 `ErrDeadlineRequired`，和 `egress` 一样，不允许一个操作无限等桶。
- **只重试可能过去的失败**：连接失败、408、429、5xx 重试，共 4 次尝试；其他 4xx（签名错、没权限、键不存在）不重试。
- **退避**：从 200ms 起翻倍，最多 5s，加一半以内的抖动；服务端给了 `Retry-After` 时按它等，最多 30s，且不会越过调用方
  的期限。
- **PUT 从头发**：请求体要求可寻址（`io.ReadSeeker`），每次重试都先 `Seek(0)`，半途失败不会把后半段当新对象写进去；
  载荷哈希在发送前算好并签名，重试沿用同一个哈希。
- **GET 不重试正文**：响应头到了以后正文读失败直接交给调用方，已经交出去的字节收不回来；调用方要完整文件就整次重来。
- **不跟随重定向**：直接 `RoundTrip`，3xx 当成错误返回，和 `egress` 的规则一致；101 升级也拒绝并关闭连接。

## 传输与安全

对象存储用自己的 `http.Transport`，不是 `egress`。`egress` 拒绝私网、回环和链路本地地址，而自托管的 SeaweedFS 正常
就在私网里，端点又是部署配置、不是用户输入，所以这里只保留相同的超时、响应头上限、连接复用和不跟随重定向，
不套地址策略。反过来，任何用户能影响的目标都不走这个客户端。

代理环境变量不读（`Proxy: nil`）：桶的地址由部署决定，不该被机器的环境改写。TLS 最低 1.2。连接和 TLS 握手各 10 秒，
空闲连接 90 秒，响应头合计最多 1 MiB，压缩关闭（对象本身通常已压缩，透明 gzip 只多花 CPU）。整个请求的期限由调用方
给。

凭证用 `secret.Secret` 保存；错误、日志和格式化输出里都是脱敏值。

## 限制

- 单个 PUT 最多 5 GiB（S3 的单次 PUT 上限）；更大的归档需要分片上传，等真正有这么大的消费者时再加。
- 只实现了 `Check`/`Put`/`Get`/`Head`/`Delete`/`List`。没有桶管理、ACL、版本、生命周期规则或多桶路由：桶由负责人建，
  保留策略属于使用它的功能。
- 没有临时凭证（`x-amz-security-token`）：部署给的是长期访问键。以后接云厂商的临时凭证时再加。

## 测试

单元测试用 `httptest` 的假 S3 覆盖期限、重试、不重试的 4xx、分页和重定向；签名另有 botocore 生成的固定向量，
固定日期、区域、路径和查询，签名逐字节对得上。

`container_test.go` 启动真实的 SeaweedFS（`chrislusf/seaweedfs:3.80`，只用于测试，不是部署依赖），用它的 shell 建桶，
再走一遍 `Check`、1 MiB 带二进制字节的 PUT、GET、HEAD、LIST 和 DELETE。手写的签名器很容易在自己的假服务器上通过，
只有真实实现能证明转义、头和分页是对的。这个测试启动容器，机器上要排在重型测试锁后面：

```sh
flock /tmp/axiom-remote-check.lock go test ./internal/platform/objectstore/...
```
