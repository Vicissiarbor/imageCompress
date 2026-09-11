# imageCompress — 简单的图片压缩服务

基于 [Gin](https://gin-gonic.com/) 的图片压缩 HTTP 服务:通过接口上传一张图片,服务对它重新编码压缩(可选等比缩小),并把压缩结果直接放在响应体里返回。**全程在内存中处理,不会把任何文件写到磁盘。**

## 功能特性

- 支持 **PNG / GIF / JPEG** 三种格式(按文件真实内容识别,不看扩展名)
- PNG 以最高压缩率(`BestCompression`)重新编码
- 可选 `scale` 参数等比缩小图片(使用 CatmullRom 高质量缩放算法)
- 读取上限 8MB,防止超大请求体拖垮服务
- 响应的 `Content-Type` 由实际解码出的格式决定,不信任客户端声明

## 快速开始

环境要求:Go 1.26+(见 `go.mod`)。

```bash
# 启动服务(监听 :46939)
go run .
```

看到 gin 的启动日志后,服务就绪。

## API 说明

### POST /compress

**请求**:`multipart/form-data`,文件字段名为 `image`。

**查询参数**:

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `scale` | 否 | 缩放比例,取值 `0 < scale ≤ 1`,如 `0.5` 表示宽高各缩一半;不传则不缩放。⚠️ 非法值(0、负数、大于 1、非数字)返回 400 |
| `quality` | JPEG 时用 | 压缩质量,1–100 的整数。**⚠️ 当前版本不生效,见[已知问题 #1](#已知问题待修复)** |

**响应**:

| 状态码 | 内容 |
| --- | --- |
| 200 | 压缩后的图片字节流,`Content-Type` 为 `image/png` / `image/gif` / `image/jpeg` |
| 400 | JSON:`{"error": "原因"}`(缺少文件、scale 非法、图片损坏等) |
| 500 | 服务内部错误(见[已知问题 #2](#已知问题待修复)) |

**curl 示例**:

```bash
# 压缩一张 PNG(结果存为 out.png)
curl "http://127.0.0.1:46939/compress" -F "image=@photo.png" -o out.png

# 缩小到一半再压缩
curl "http://127.0.0.1:46939/compress?scale=0.5" -F "image=@photo.png" -o out.png

# 压缩 JPEG —— 当前版本会返回 400,原因见已知问题 #1
curl "http://127.0.0.1:46939/compress?quality=80" -F "image=@photo.jpg" -o out.jpg
```

**格式行为说明**:

- 输入 PNG → 输出 PNG;输入 GIF → 输出 GIF;输入 JPEG → 输出 JPEG
- **GIF 只保留第一帧**,动图压缩后会变成静态图
- 其他格式(BMP、WebP 等)未注册解码器,会被当作无效图片拒绝

## 测试

测试位于 `main_test.go`,通过 `httptest` 直接调用 handler,不需要真的启动服务:

```bash
go test ./...        # 运行全部测试
go test -v ./...     # 查看每个用例的详细结果
```

覆盖场景:PNG/GIF 的压缩与缩放、JPEG 当前行为、非法 `scale`、缺少文件字段、损坏图片。
其中标注了「已知问题」的用例是**如实记录当前行为**的,修复代码后请按测试注释里的提示同步更新期望值。

## 已知问题(待修复)

以下问题已在 `main_test.go` 中用「已知问题」标注并记录,修复时对照即可:

1. **JPEG 压缩不可用** — `main.go:88` 把质量参数错读成了 `c.Query("scale")`(应为 `"quality"`),导致 JPEG 上传几乎总是返回 400;只有 `?scale=1` 能"碰巧成功"(被误当成 `quality=1`,最低质量)。
2. **损坏图片 + scale 参数 → 500** — `main.go:62` 的 `image.Decode` 之后没有检查 `err`,解码失败时 `img` 为 nil,缩放时 `img.Bounds()` 空指针 panic(被 gin 的 Recovery 兜住返回 500,进程不会挂)。
3. **JPEG 编码错误被遮蔽** — `main.go:89` 用 `:=` 声明了新的 `err`,导致 `jpeg.Encode` 万一失败时,`main.go:100` 的检查捕获不到,会把不完整的响应以 200 发出去。
4. **`scale=NaN` 绕过校验** — `?scale=NaN` 能通过 `0 < scale ≤ 1` 的检查,最终产出一张 1×1 的图片(无害但行为怪异)。

## 安全说明

**已有的防护**:

- `io.LimitReader` 限制最多读取 8MB,`MaxMultipartMemory` 限制 16MB
- 输出格式由真实解码结果决定,客户端无法通过伪造 Content-Type/扩展名让服务返回错误类型
- 不写文件到磁盘,不存在路径穿越问题

**尚未处理的风险**(部署到非本机环境前请先解决):

- **图片炸弹(最主要风险)**:8MB 的 PNG 可以解码出超大尺寸的图(例如 20000×20000 ≈ 1.6GB 内存),少量并发请求即可耗尽服务器内存。推荐做法:解码前先用 `image.DecodeConfig` 只读尺寸,超限直接拒绝:

  ```go
  cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
  if err != nil {
      // 返回 400
  }
  if cfg.Width > 4096 || cfg.Height > 4096 {
      // 返回 400:图片尺寸超限
  }
  ```

- **监听所有网卡且无鉴权/无限流/无 TLS**:`Run(":46939")` 会绑定 0.0.0.0,公网服务器上任何人都能调用。只在本机使用可改为 `Run("127.0.0.1:46939")`;要对外服务则应加上鉴权、限流,并放在 HTTPS 反向代理之后。
- **日志注入**:上传的文件名未经处理直接 `log.Println`,文件名里带换行符可以伪造日志行。
- 无认证的公开部署还会面临滥用风险(拿它当免费图床/压测靶子),产生带宽与 CPU 消耗。

## 项目结构

```
.
├── main.go        # 服务入口与压缩逻辑
├── main_test.go   # handler 测试(httptest,无需启动真实服务)
├── go.mod / go.sum
└── README.md
```
