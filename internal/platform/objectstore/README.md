# 对象存储

用 S3 接口读写对象。程序里只有这一份对象存储客户端；逻辑备份任务也用它，但用自己的桶和凭证（`deploy/ops`，见
[备份与恢复](../../../docs/ops/backup-restore.md#与程序的对象存储分开)）。WAL 归档不走这个客户端：pgBackRest 在数据库镜像里
用自己的 S3 实现写同一个桶的 `<AXIOM_BACKUP_WAL_PREFIX>`（默认 `<前缀>-pgbackrest`），见[WAL 连续归档与时间点恢复](../../../docs/ops/backup-restore.md#wal-连续归档与时间点恢复)。
observability 的调用记录保留任务用它写每月的归档；operations 用它把审计链复制到部署之外的副本桶（另一组变量，见下文「配置」）；对话正文的写入还不经过它（保存正文的平台开关仍然关着），受控导出用它的 `Get` 读正文对象（[记录与告警](../../../docs/domains/observability.md#存储)）。

架构上的选型是「只依赖 S3 接口」，云厂商的对象存储和自托管的 SeaweedFS 都能接（[总体架构的技术选型](../../../docs/architecture.md#技术选型)）。本包不引入
AWS SDK：用到的只是签名、五个对象操作和一次列举，自己实现 SigV4 少一批依赖；签名与 S3 的互操作由真实的 SeaweedFS
容器测试守住。

## 接口

```go
type Config struct { Endpoint, Bucket, Region, AccessKeyID string; SecretAccessKey secret.Secret; PathStyle bool }
func ReadConfig(env *config.Env) (Config, error)   // 一个变量都没设时返回零值，表示关闭
func ReadConfigAt(env *config.Env, prefix string) (Config, error) // 同样的六个变量，名字以 prefix 开头
func Open(cfg Config) (*Client, error)

func (c *Client) Create(ctx, allowExisting bool) (bool, error) // 先 HEAD；404 才 PUT，返回是否新建；已存在需显式同意
func (c *Client) Check(ctx) error                  // HEAD 桶：核对桶存在、凭证可用，不读不写
func (c *Client) Put(ctx, Upload) (ObjectInfo, error)
func (c *Client) Get(ctx, key) (*Object, error)    // 调用方关闭 Body
func (c *Client) Head(ctx, key) (ObjectInfo, error)
func (c *Client) Delete(ctx, key) error            // 已经不存在的键也算成功
func (c *Client) List(ctx, ListOptions) (ListPage, error)
```

`Create`/`Check` 的 HEAD 只证明这些凭证能访问桶，不能证明桶归调用方所有：只对自有的桶运行，桶策略要限制到这些凭证。
`axiom objectstore create` 对已存在的桶默认报错，确认自有后加 `--allow-existing`；它不会因此写入或改 ACL。

`Upload.NoOverwrite` 发 `If-None-Match: *`（签进签名）：键已存在时桶答 412，`Put` 返回可用 `errors.Is(err, ErrExists)`
判断的错误，不覆盖；这样的上传总是一次 PUT，因为 multipart 只在完成时才检查条件。不是每个实现都执行这个条件：
SeaweedFS 3.80 照收不误、照样覆盖，所以调用方不能只靠它，审计链的副本在写前先读这个键。

每个带正文的请求（单次 PUT、multipart 的每一片、建桶）都带 S3 的内容校验 `x-amz-sdk-checksum-algorithm: SHA256` 和
`x-amz-checksum-sha256`（就是 SigV4 已经算出的那个 SHA-256 的 base64，签进签名）：开了 Object Lock 默认保留期的 S3 桶
要求上传带 `Content-MD5` 或这样的校验，没有就拒绝；别的桶也用它核对收到的字节。用 SHA-256 而不用 MD5，是因为它已经算好、
不必再读一遍，也不引入弱哈希。

`List` 一次返回一页和下一页的令牌；要整个前缀的调用方顺着 `NextToken` 循环。`Upload.Size` 在调用方知道字节数时给出
（文件 `Stat` 一般知道），`-1` 表示由客户端边算哈希边数；给了但对不上是错误，不发请求。知道大小且超过 16 MiB 时走
multipart，`-1` 仍是单次 PUT（最多 5 GiB）。

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

副本桶用 `ReadConfigAt(env, "AXIOM_AUDIT_REPLICA")` 读 `AXIOM_AUDIT_REPLICA_ENDPOINT` 等同样的六个变量，规则相同，错误报的是
这一组的变量名。它的凭证与归档的分开：归档要删过期对象，副本的凭证能写、读和列举，不能删、不能覆盖（S3 对没有列举权限的调用方把不存在的键答 403）（[运营的「审计」](../../../docs/domains/operations.md#审计)）。

`serve` 启动时读取并校验这组变量：设了但不成组会启动失败，全不设则对象存储关闭，调用记录的保留任务只预建分区、
不删除任何东西（[observability 的分区](../../../internal/observability/README.md#分区)）。程序里的写入方是调用记录的
归档；`axiom objectstore` 是运维调用方。备份任务用同一份客户端、同一组变量名，但桶和凭证由 `deploy/compose.yaml`
从 `AXIOM_BACKUP_S3_*` 映射进 `ops` 容器，程序容器拿不到备份的凭证。

## 期限与重试

- **每次操作都要期限**：`ctx` 上没有 deadline 时返回 `ErrDeadlineRequired`，和 `egress` 一样，不允许一个操作无限等桶。
- **只重试可能过去的失败**：连接失败、408、429、5xx 重试，共 4 次尝试；其他 4xx（签名错、没权限、键不存在）不重试。
- **退避**：从 200ms 起翻倍，最多 5s，加一半以内的抖动；服务端给了 `Retry-After` 时按它等，最多 30s，且不会越过调用方
  的期限。
- **PUT 从头发**：请求体要求可寻址（`io.ReadSeeker`），哈希和每次发送都先 `Seek(0)`，半途失败不会把后半段当新对象写进去；
  载荷哈希在发送前算好并签名，重试沿用同一个哈希。multipart 的每一片都从对象里的固定偏移读，重试不会串到下一片。
- **PUT 先查期限和上限**：读请求体之前检查调用方的期限和取消，哈希分块检查 `ctx`，最多读上限加一个字节；单次 PUT 超过
  5 GiB 返回 `ErrTooLarge`，已知大小超过 5 TiB 直接拒绝。失败的上传会 abort，不把分片留在桶里。底层 `Read` 如果自己
  阻塞，`context` 不能替它解阻塞；调用方要能关闭文件或提供响应取消的 reader，这里不启动后台 goroutine 假装取消。
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

- 单个 PUT 最多 5 GiB，multipart 对象最多 5 TiB、最多 10,000 片；分片默认 16 MiB，对象大到 160 GiB 以上时按 1 MiB
  向上取整自动放大。最后一片可以小于 5 MiB，其余分片不小于 5 MiB（由 S3 检查，客户端不自造限制）。
- 实现了 `Create`、`Check`、`Put`、`Get`、`Head`、`Delete`、`List`。没有 ACL、版本、生命周期规则或多桶路由；桶由负责人
  建（`Create` 只是让部署能把自己拥有的桶准备好），保留策略属于使用它的功能。
- 没有临时凭证（`x-amz-security-token`）：部署给的是长期访问键。以后接云厂商的临时凭证时再加。

## 测试

单元测试用 `httptest` 的假 S3 覆盖期限、重试、不重试的 4xx、分页、重定向、multipart 的分片与 abort；签名另有
botocore 生成的固定向量，固定日期、区域、路径和查询，签名逐字节对得上。

`container_test.go` 启动真实的 SeaweedFS（`chrislusf/seaweedfs:3.80`，只用于测试，不是部署依赖），用它的 shell 建主桶，
再走一遍 `Create`、`Check`、1.25 MiB 带二进制字节的 PUT、GET、HEAD、LIST、DELETE 和带 `If-None-Match` 签名的 PUT，以及 25 MiB 的 multipart PUT 与逐字节
比对；另有一个桶只由 `Create` 建两次（第二次返回 false），证明真实服务器上的 HEAD/PUT 路径。
手写的签名器很容易在自己的假服务器上通过，只有真实实现能证明转义、头和分页是对的。这个测试启动容器，机器上要排在
重型测试锁后面：

```sh
flock /tmp/axiom-remote-check.lock go test ./internal/platform/objectstore/...
```
