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
	"testing"

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

// newRouter 构建一个与 main() 完全一致的路由,供测试使用。
func newRouter() http.Handler {
	r := gin.Default()
	r.MaxMultipartMemory = 16 << 20
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

// TestCompressHardler_PNG 测试 PNG 上传:默认压缩、按比例缩放。
func TestCompressHardler_PNG(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name   string
		query  string
		wantW  int
		wantH  int
	}{
		{name: "默认压缩不缩放", query: "", wantW: 100, wantH: 80},
		{name: "scale=0.5缩放一半", query: "?scale=0.5", wantW: 50, wantH: 40},
		{name: "scale=1.0保持原尺寸", query: "?scale=1.0", wantW: 100, wantH: 80},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.png", makePNG(t, 100, 80))
			if rec.Code != http.StatusOK {
				checkErrorJSON(t, rec)
				t.Fatalf("状态码 = %d, 期望 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
				t.Errorf("Content-Type = %q, 期望 image/png", ct)
			}
			img, format := decodeBody(t, rec)
			if format != "png" {
				t.Errorf("返回图片格式 = %q, 期望 png", format)
			}
			checkDimensions(t, img, tt.wantW, tt.wantH)
		})
	}
}

// TestCompressHardler_GIF 测试 GIF 上传:默认压缩、按比例缩放。
// 注意:当前实现只保留 GIF 的第一帧,动画会丢失(README 中已说明)。
func TestCompressHardler_GIF(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name  string
		query string
		wantW int
		wantH int
	}{
		{name: "默认压缩不缩放", query: "", wantW: 60, wantH: 40},
		{name: "scale=0.5缩放一半", query: "?scale=0.5", wantW: 30, wantH: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.gif", makeGIF(t, 60, 40))
			if rec.Code != http.StatusOK {
				checkErrorJSON(t, rec)
				t.Fatalf("状态码 = %d, 期望 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "image/gif" {
				t.Errorf("Content-Type = %q, 期望 image/gif", ct)
			}
			img, format := decodeBody(t, rec)
			if format != "gif" {
				t.Errorf("返回图片格式 = %q, 期望 gif", format)
			}
			checkDimensions(t, img, tt.wantW, tt.wantH)
		})
	}
}

// TestCompressHardler_JPEG 测试 JPEG 上传。
//
// 【已知问题#1】main.go 第88行把质量参数错读成了 c.Query("scale")(应为 "quality"),
// 导致当前版本 JPEG 上传几乎总是返回 400;只有 ?scale=1 能"碰巧成功"
// (字符串 "1" 被误当成 quality=1,即最低质量)。
// 本测试如实记录当前的实际行为。将来修复参数名后,请把期望改为:
// ?quality=80 → 200、不传参数 → 200(使用默认质量)。
func TestCompressHardler_JPEG(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "已知问题#1:quality=80被忽略_返回400", query: "?quality=80", wantStatus: http.StatusBadRequest},
		{name: "已知问题#1:scale=0.5加quality=80_返回400", query: "?scale=0.5&quality=80", wantStatus: http.StatusBadRequest},
		{name: "已知问题#1:不传参数_返回400", query: "", wantStatus: http.StatusBadRequest},
		{name: "已知问题#1:scale=1被误读成quality=1_返回200", query: "?scale=1", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.jpg", makeJPEG(t, 100, 80))
			if rec.Code != tt.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusBadRequest {
				checkErrorJSON(t, rec)
				return
			}
			// ?scale=1 的"碰巧成功"路径:应返回合法 JPEG,尺寸不变。
			if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
				t.Errorf("Content-Type = %q, 期望 image/jpeg", ct)
			}
			img, format := decodeBody(t, rec)
			if format != "jpeg" {
				t.Errorf("返回图片格式 = %q, 期望 jpeg", format)
			}
			checkDimensions(t, img, 100, 80)
		})
	}
}

// TestCompressHardler_InvalidScale 测试非法 scale 参数应当返回 400。
func TestCompressHardler_InvalidScale(t *testing.T) {
	router := newRouter()

	tests := []struct {
		name  string
		query string
	}{
		{name: "scale=0", query: "?scale=0"},
		{name: "scale为负数", query: "?scale=-0.5"},
		{name: "scale大于1", query: "?scale=1.5"},
		{name: "scale不是数字", query: "?scale=abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "test.png", makePNG(t, 100, 80))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400", rec.Code)
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

// TestCompressHardler_CorruptImage 测试上传损坏/非图片内容。
//
// 【已知问题#2】main.go 第62行 image.Decode 之后没有检查 err,
// 解码失败时 img 为 nil;若请求带了 scale 参数,缩放时 img.Bounds()
// 会触发空指针 panic(被 gin.Default 的 Recovery 兜住,进程不挂但返回 500)。
// 本测试如实记录当前行为。将来加上解码错误检查后,
// 带 scale 的用例期望应改为 400。
func TestCompressHardler_CorruptImage(t *testing.T) {
	router := newRouter()
	garbage := []byte("这根本不是一张图片,只是一段普通文字")

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		// 不带 scale 时不会碰到 nil img,恰好走进 quality 校验分支返回 400。
		{name: "不带scale参数_返回400", query: "", wantStatus: http.StatusBadRequest},
		// 带 scale 时对 nil img 调 Bounds() → panic → Recovery → 500。
		{name: "已知问题#2:带scale=0.5触发panic_返回500", query: "?scale=0.5", wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postImage(t, router, tt.query, "image", "fake.png", garbage)
			if rec.Code != tt.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusBadRequest {
				checkErrorJSON(t, rec)
			}
		})
	}
}
