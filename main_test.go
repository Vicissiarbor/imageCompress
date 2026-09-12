package main

import (
	"bytes"
	"encoding/json"
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
	"strings"
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
// 注意:修复已知问题#1后,quality 参数对所有格式都是必填的
// (PNG/GIF 编码时并不使用它,但不传会在校验阶段被拒)。
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
		{name: "不传quality返回400", query: "", wantStatus: 400},
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
		{name: "不传quality返回400", query: "", wantStatus: 400},
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

// TestCompressHardler_ScaleNaN 记录【已知问题#4(遗留)】的当前行为:
// main.go:86 用 scale==math.NaN() 判断 NaN,但 Go(IEEE 754)里 NaN 与
// 任何值比较都是 false——包括它自己——所以这个判断永远不成立,NaN 依然
// 通过校验。缩放时 int(尺寸*NaN) 得到平台相关的极值(amd64 上是极小负数),
// 被 max(w,1) 钳制成 1,最终返回一张 1×1 的图片。
// 正确写法是 math.IsNaN(scale)。修复后请把本用例期望改为 400,
// 并可以直接并回 TestCompressHardler_InvalidScale 的表格。
func TestCompressHardler_ScaleNaN(t *testing.T) {
	router := newRouter()

	rec := postImage(t, router, "?scale=NaN&quality=80", "image", "test.png", makePNG(t, 100, 80))
	checkSuccessImage(t, rec, "image/png", "png", 1, 1)
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

// TestRateLimitMiddleware_NoToken 测试没有令牌时返回 429。
// 用手动创建的通道(不启动补充协程)保证结果完全确定。
//
// 【已知问题#5】429 分支缺少 c.Abort():按 gin 的机制,中间件返回后
// compressHardler 仍会完整执行一遍——被"拒绝"的请求照样消耗全部
// CPU/内存,且响应体在 JSON 错误后面还追加了 PNG 字节(脏响应)。
// 修法:在 c.JSON 之后加 c.Abort(),或改用 c.AbortWithStatusJSON。
// 本测试当前记录现状;修复 Abort 后请把"body 更长"的断言改为
// "body 恰好等于 JSON 错误"。
func TestRateLimitMiddleware_NoToken(t *testing.T) {
	ch := make(chan struct{}, 1) // 空通道 = 没有令牌
	router := newRouterWithLimit(ch)

	rec := postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 10, 10))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", rec.Code)
	}

	const wantPrefix = `{"error":"Too many requests."}`
	body := rec.Body.String()
	if !strings.HasPrefix(body, wantPrefix) {
		t.Fatalf("响应体不以期望的JSON开头, 实际: %q", body)
	}
	if len(body) == len(wantPrefix) {
		t.Errorf("已知问题#5的当前行为是:handler仍会执行并在JSON后追加图片字节," +
			"现在body只剩JSON了——如果你已经加了c.Abort(),请同步把本断言改为body恰好等于JSON")
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
func TestRateLimitMiddleware_TokenInit(t *testing.T) {
	tokens := tokenInit() // 后台协程每 333ms 尝试补一个令牌
	router := newRouterWithLimit(tokens)

	doReq := func() int {
		return postImage(t, router, "?quality=80", "image", "test.png", makePNG(t, 10, 10)).Code
	}

	// 1) 通道初始为空(第一个令牌要等第一次 tick),轮询等待拿到首个令牌。
	gotToken := false
	for i := 0; i < 20; i++ {
		if doReq() == http.StatusOK {
			gotToken = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gotToken {
		t.Fatal("2秒内始终没有拿到令牌, tokenInit 的补充协程可能没有工作")
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
