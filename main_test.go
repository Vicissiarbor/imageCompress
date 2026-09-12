package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestMain 做全局测试准备:
// 让 gin 进入测试模式,并把 gin 自带的请求日志丢弃,保持测试输出干净。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	os.Exit(m.Run())
}

// newRouter 构建只含 compressHardler 的路由(不带限速中间件)。
// handler 测试故意不挂 RateLimitMiddleware,避免令牌补充的时序干扰用例;
// 限速行为由 TestRateLimit* 专门测试。
func newRouter() http.Handler {
	r := gin.Default()
	r.MaxMultipartMemory = 16 << 20
	r.POST("/compress", compressHardler)
	return r
}

// newRouterWithLimit 构建带限速中间件的路由,接线方式与 main() 一致。
// ch 由调用方提供,便于构造确定性的令牌状态。
func newRouterWithLimit(ch chan struct{}) http.Handler {
	r := gin.Default()
	r.MaxMultipartMemory = 16 << 20
	r.Use(RateLimitMiddleware(ch))
	r.POST("/compress", compressHardler)
	return r
}

// testImage 生成一张 w×h 的渐变色测试图,避免纯色图让压缩测试失真。
func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: 128,
				A: 255,
			})
		}
	}
	return img
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(w, h)); err != nil {
		t.Fatalf("生成PNG测试图失败: %v", err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(w, h), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("生成JPEG测试图失败: %v", err)
	}
	return buf.Bytes()
}

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, testImage(w, h), nil); err != nil {
		t.Fatalf("生成GIF测试图失败: %v", err)
	}
	return buf.Bytes()
}

// postImage 模拟浏览器用 multipart 表单上传图片。
// field 传空字符串表示请求里不带任何文件字段。
func postImage(t *testing.T, router http.Handler, query, field, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if field != "" {
		fw, err := w.CreateFormFile(field, filename)
		if err != nil {
			t.Fatalf("构建表单失败: %v", err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("写入文件内容失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/compress"+query, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// decodeBody 校验响应体是一张合法图片,并返回解码结果和格式。
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) (image.Image, string) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		body := rec.Body.Bytes()
		if len(body) > 100 {
			body = body[:100]
		}
		t.Fatalf("响应体不是合法图片: %v (body前100字节: %q)", err, body)
	}
	return img, format
}

// checkErrorJSON 校验错误响应是 JSON,且带有非空的 error 字段。
func checkErrorJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("期望JSON错误响应, 解析失败: %v (body: %q)", err, rec.Body.String())
	}
	if payload.Error == "" {
		t.Errorf("期望error字段非空, 实际body: %q", rec.Body.String())
	}
}

// checkDimensions 校验返回图片的尺寸。
func checkDimensions(t *testing.T, img image.Image, wantW, wantH int) {
	t.Helper()
	b := img.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("返回图片尺寸 = %dx%d, 期望 %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
}

// checkSuccessImage 校验成功响应:状态码、Content-Type、可解码、格式、尺寸。
func checkSuccessImage(t *testing.T, rec *httptest.ResponseRecorder, wantMime, wantFormat string, wantW, wantH int) {
	t.Helper()
	if rec.Code != http.StatusOK {
		checkErrorJSON(t, rec)
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != wantMime {
		t.Errorf("Content-Type = %q, 期望 %q", ct, wantMime)
	}
	img, format := decodeBody(t, rec)
	if format != wantFormat {
		t.Errorf("返回图片格式 = %q, 期望 %q", format, wantFormat)
	}
	checkDimensions(t, img, wantW, wantH)
}

// TestCompressHardler_PNG 测试 PNG 上传。
// quality 参数对所有格式都可选:缺省时 main.go 使用默认值 100
// (上一版"默认值被 Atoi 失败覆盖"的死代码问题已修复);
// PNG/GIF 编码并不使用该值,传了只会被校验合法性。
func TestCompressHardler_PNG(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantW      int
		wantH      int
	}{
		{name: "quality=80压缩不缩放", query: "?quality=80", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "scale=0.5缩放一半", query: "?scale=0.5&quality=80", wantStatus: 200, wantW: 50, wantH: 40},
		{name: "scale=1.0保持原尺寸", query: "?scale=1.0&quality=80", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "不传quality使用默认100", query: "", wantStatus: 200, wantW: 100, wantH: 80},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.png", makePNG(t, 100, 80))
			if tt.wantStatus == 400 {
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("状态码 = %d, 期望 400", rec.Code)
				}
				checkErrorJSON(t, rec)
				return
			}
			checkSuccessImage(t, rec, "image/png", "png", tt.wantW, tt.wantH)
		})
	}
}

// TestCompressHardler_GIF 测试 GIF 上传。
// 注意:当前实现只保留 GIF 的第一帧,动画会丢失(README 中已说明)。
func TestCompressHardler_GIF(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name  string
		query string
		wantW int
		wantH int
	}{
		{name: "quality=80压缩不缩放", query: "?quality=80", wantW: 60, wantH: 40},
		{name: "scale=0.5缩放一半", query: "?scale=0.5&quality=80", wantW: 30, wantH: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.gif", makeGIF(t, 60, 40))
			checkSuccessImage(t, rec, "image/gif", "gif", tt.wantW, tt.wantH)
		})
	}
}

// TestCompressHardler_JPEG 测试 JPEG 上传(验证已知问题#1的修复:
// quality 参数现在从 c.Query("quality") 读取,1-100 的整数)。
func TestCompressHardler_JPEG(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantW      int
		wantH      int
	}{
		{name: "quality=80压缩", query: "?quality=80", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "scale=0.5加quality=80", query: "?scale=0.5&quality=80", wantStatus: 200, wantW: 50, wantH: 40},
		{name: "quality=1最低质量", query: "?quality=1", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "quality=100最高质量", query: "?quality=100", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "不传quality使用默认100", query: "", wantStatus: 200, wantW: 100, wantH: 80},
		{name: "quality=0越界返回400", query: "?quality=0", wantStatus: 400},
		{name: "quality=101越界返回400", query: "?quality=101", wantStatus: 400},
		{name: "quality不是整数返回400", query: "?quality=abc", wantStatus: 400},
		{name: "quality是小数返回400", query: "?quality=80.5", wantStatus: 400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.jpg", makeJPEG(t, 100, 80))
			if tt.wantStatus == 400 {
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("状态码 = %d, 期望 400", rec.Code)
				}
				checkErrorJSON(t, rec)
				return
			}
			checkSuccessImage(t, rec, "image/jpeg", "jpeg", tt.wantW, tt.wantH)
		})
	}
}

// TestCompressHardler_InvalidScale 测试非法 scale 参数应当返回 400。
// 所有用例都带上合法的 quality=80,确保触发 400 的是 scale 校验
// 而不是 quality 校验(scale 校验在前,但带上 quality 更严谨)。
func TestCompressHardler_InvalidScale(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name  string
		query string
	}{
		{name: "scale=0", query: "?scale=0&quality=80"},
		{name: "scale为负数", query: "?scale=-0.5&quality=80"},
		{name: "scale大于1", query: "?scale=1.5&quality=80"},
		{name: "scale不是数字", query: "?scale=abc&quality=80"},
		// 已知问题#4已修复:改用 math.IsNaN(scale) 后,NaN 能被正确拒绝。
		{name: "scale=NaN被拒绝", query: "?scale=NaN&quality=80"},
		// 补充边界:±Inf 也应当被范围校验拒绝(ParseFloat 对 Inf 不报错)。
		{name: "scale=Inf被拒绝", query: "?scale=Inf&quality=80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.png", makePNG(t, 100, 80))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400 (若为200说明非法scale通过了校验)", rec.Code)
			}
			checkErrorJSON(t, rec)
		})
	}
}

// TestCompressHardler_MissingFile 测试请求里没有图片文件时应当返回 400。
func TestCompressHardler_MissingFile(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name  string
		field string
	}{
		{name: "完全不带文件字段", field: ""},
		{name: "字段名不是image", field: "file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, "", tt.field, "test.png", makePNG(t, 10, 10))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400", rec.Code)
			}
			checkErrorJSON(t, rec)
		})
	}
}

// TestCompressHardler_CorruptImage 测试上传损坏/非图片内容时应当返回 400,
// 服务不能崩溃(验证已知问题#2的修复:image.Decode 的错误现在被检查了,
// 带 scale 参数也不再 panic 返回 500)。
func TestCompressHardler_CorruptImage(t *testing.T) {
	router := newRouter()
	garbage := []byte("这根本不是一张图片,只是一段普通文字")

	tests := []struct {
		name  string
		query string
	}{
		{name: "不带scale参数", query: "?quality=80"},
		{name: "带scale=0.5参数也不再panic", query: "?scale=0.5&quality=80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "fake.png", garbage)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400 (500通常意味着handler内部panic了)", rec.Code)
			}
			checkErrorJSON(t, rec)
		})
	}
}

// makePNGHeader 手工拼装一个"PNG 签名 + 合法 IHDR 头 + 垃圾正文"的文件。
// 这样测试里能廉价地构造"声明尺寸巨大"的解压炸弹图片:
// image.DecodeConfig 只读 IHDR(会校验 CRC),不需要真的生成几千万像素。
func makePNGHeader(t *testing.T, width, height uint32) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) // PNG 签名

	ihdr := make([]byte, 0, 13)
	ihdr = binary.BigEndian.AppendUint32(ihdr, width)
	ihdr = binary.BigEndian.AppendUint32(ihdr, height)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // 位深8、真彩色、压缩/过滤/隔行均默认

	var chunkLen [4]byte
	binary.BigEndian.PutUint32(chunkLen[:], uint32(len(ihdr)))
	buf.Write(chunkLen[:])
	buf.WriteString("IHDR")
	buf.Write(ihdr)

	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	buf.Write(crc[:])

	buf.Write(bytes.Repeat([]byte{0xff}, 32)) // IHDR 之后全是坏字节,完整解码必然失败
	return buf.Bytes()
}

// TestCompressHardler_ImageTooLarge 测试防"图片炸弹"的尺寸上限:
// main.go 在完整解码前先用 image.DecodeConfig 读头部尺寸,
// 宽或高 > 4096 直接返回 400 "Image is too large."。
// (炸弹图用 makePNGHeader 构造,小图用例已由上面的测试覆盖。)
func TestCompressHardler_ImageTooLarge(t *testing.T) {
	router := newRouter()

	const tooLargeMsg = "Image is too large."

	t.Run("声明5000x5000的炸弹图被尺寸校验拒绝", func(t *testing.T) {
		rec := postImage(t, router, "?quality=80", "image", "bomb.png", makePNGHeader(t, 5000, 5000))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d, 期望 400", rec.Code)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("解析错误响应失败: %v (body: %q)", err, rec.Body.String())
		}
		if payload.Error != tooLargeMsg {
			t.Errorf("error = %q, 期望 %q(应被尺寸上限拦截,而不是走到完整解码)", payload.Error, tooLargeMsg)
		}
	})

	t.Run("边界4096x4096不被尺寸校验拒绝", func(t *testing.T) {
		// 这个夹具只有合法头部、正文是坏字节:它会"通过"尺寸校验,
		// 然后在完整解码时报 PNG 格式错误。只要错误不是
		// "Image is too large.",就证明 4096(含)是被放行的。
		rec := postImage(t, router, "?quality=80", "image", "edge.png", makePNGHeader(t, 4096, 4096))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d, 期望 400(坏正文应报解码错误)", rec.Code)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("解析错误响应失败: %v (body: %q)", err, rec.Body.String())
		}
		if payload.Error == tooLargeMsg {
			t.Errorf("4096x4096 应当通过尺寸校验(上限为 >4096 才拒绝),却收到 %q", payload.Error)
		}
	})
}

// TestRateLimitMiddleware_NoToken 测试没有令牌时返回 429。
// 用手动创建的通道(不启动补充协程)保证结果完全确定。
//
// 已知问题#5已修复:429 分支现在会在 c.JSON 之后调用 c.Abort(),
// handler 不再执行——响应体应当是"恰好等于"的纯 JSON 错误,
// 后面不能再粘任何图片字节。
func TestRateLimitMiddleware_NoToken(t *testing.T) {
	ch := make(chan struct{}, 1) // 空通道 = 没有令牌
	router := newRouterWithLimit(ch)

	rec := postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 10, 10))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", rec.Code)
	}

	const wantJSON = `{"error":"Too many requests."}`
	if body := rec.Body.String(); body != wantJSON {
		t.Fatalf("响应体 = %q, 期望恰好等于 %q(若后面粘了图片字节,说明 c.Abort() 又丢了)", body, wantJSON)
	}
}

// TestRateLimitMiddleware_WithToken 测试有令牌时请求正常进入 handler(全链路)。
func TestRateLimitMiddleware_WithToken(t *testing.T) {
	ch := make(chan struct{}, 1)
	ch <- struct{}{} // 预放一个令牌
	router := newRouterWithLimit(ch)

	rec := postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 20, 20))
	checkSuccessImage(t, rec, "image/png", "png", 20, 20)

	// 令牌应已被消耗:紧接着的第二个请求必须 429(手动通道没有补充协程)。
	rec2 := postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 20, 20))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("第二个请求状态码 = %d, 期望 429 (令牌应已耗尽)", rec2.Code)
	}
}

// TestRateLimitMiddleware_TokenInit 测试 tokenInit 的令牌自动补充:
// 每 1/3 秒补一个令牌,通道容量为 1(即平均限速约 3 请求/秒,突发上限 1)。
// 已知问题#6已修复:tokenInit 现在预放了一个初始令牌,首个请求立即成功。
func TestRateLimitMiddleware_TokenInit(t *testing.T) {
	tokens := tokenInit() // 后台协程每 333ms 尝试补一个令牌
	router := newRouterWithLimit(tokens)

	doReq := func() int {
		return postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 10, 10)).Code
	}

	// 1) 通道已预放初始令牌:首个请求应当立即 200,无需等待。
	if code := doReq(); code != http.StatusOK {
		t.Fatalf("首个请求状态码 = %d, 期望 200(初始令牌应已预放进通道)", code)
	}

	// 2) 令牌刚被消耗,立刻连发 3 个请求:即使期间恰好 tick 补充了 1 个,
	//    容量为 1 也最多放行 1 个,因此至少 2 个应当 429。
	rejected := 0
	for i := 0; i < 3; i++ {
		if doReq() == http.StatusTooManyRequests {
			rejected++
		}
	}
	if rejected < 2 {
		t.Errorf("连发3个请求被拒 %d 个, 期望至少 2 个 429", rejected)
	}

	// 3) 等 500ms(必有一次 tick),令牌应当已补充,请求恢复 200。
	time.Sleep(500 * time.Millisecond)
	if code := doReq(); code != http.StatusOK {
		t.Fatalf("等待补充后状态码 = %d, 期望 200", code)
	}
}
