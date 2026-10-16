// Package webui 提供内嵌前端静态资源服务与 SPA 路由 fallback。
//
// 生产产物写入 internal/webui/dist/ 并由 embed.FS 内嵌,随单个 Go 二进制分发。
// 文本资源(JS/CSS/HTML/SVG/JSON)在客户端声明 Accept-Encoding: gzip 时以
// gzip 传输: 生产 bundle 674KB → 约 211KB(实测), 首屏传输量降低约 69%。
// 嵌入资源不可变, 压缩结果按文件名缓存一次, 后续请求零压缩开销。
package webui

import (
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed dist/*
var embedded embed.FS

// dist 是内嵌的 dist 子文件系统。
var dist fs.FS

func init() {
	dist, _ = fs.Sub(embedded, "dist")
}

// Embedded 返回内嵌的 dist 文件系统。
func Embedded() (fs.FS, error) {
	if dist == nil {
		return nil, errors.New("前端资源未构建")
	}
	return dist, nil
}

// gzipMinBytes 是启用压缩的最小体积: 小文件压缩收益不足以抵消头部开销。
const gzipMinBytes = 1024

// gzipCache 缓存文本资源的预压缩结果(嵌入资源不可变, 压缩一次即可复用)。
type gzipCache struct {
	mu    sync.Mutex
	items map[string][]byte
}

// compressed 返回 name 对应内容的 gzip 结果(首次压缩后缓存)。
func (g *gzipCache) compressed(name string, data []byte) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok := g.items[name]; ok {
		return b
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	if g.items == nil {
		g.items = make(map[string][]byte)
	}
	g.items[name] = buf.Bytes()
	return buf.Bytes()
}

// compressibleType 判断 MIME 类型是否值得 gzip(文本类; 字体/图片已是压缩格式)。
func compressibleType(ctype string) bool {
	mime := strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0])
	switch mime {
	case "text/javascript", "text/css", "text/html", "text/plain",
		"application/json", "image/svg+xml":
		return true
	}
	return false
}

// acceptsGzip 解析 Accept-Encoding, 判断客户端是否接受 gzip(含 q=0 拒绝语义)。
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		token := strings.ToLower(strings.TrimSpace(fields[0]))
		if token != "gzip" && token != "x-gzip" {
			continue
		}
		// 检查 q=0(显式拒绝)。缺省 q 视为 1。
		for _, param := range fields[1:] {
			kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(kv) == 2 && strings.TrimSpace(kv[0]) == "q" {
				if strings.TrimSpace(kv[1]) == "0" || strings.TrimSpace(kv[1]) == "0.0" || strings.TrimSpace(kv[1]) == "0.00" {
					return false
				}
			}
		}
		return true
	}
	return false
}

// mimeByExt 返回常见扩展名对应的 Content-Type。
func mimeByExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".woff"):
		return "font/woff"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// hashedAsset 判断路径是否含哈希文件名(如 app-abc123.js)。
func hashedAsset(name string) bool {
	base := path.Base(name)
	dot := strings.Index(base, ".")
	if dot < 0 {
		return false
	}
	return len(base[:dot]) > 1 && strings.Contains(base[:dot], "-")
}

// Handler 返回服务内嵌静态资源并支持 SPA fallback 的 http.Handler。
//
// 只允许 GET/HEAD;路径先 path.Clean 并拒绝 "..";存在文件就按扩展名服务,
// 不存在且路径不含文件扩展名时返回 index.html;生产 dist 未生成时返回
// 清晰的 503 中文纯文本,而不是 panic。文本资源在客户端接受 gzip 时压缩传输。
func Handler(fsys fs.FS) http.Handler {
	var gz gzipCache
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		if strings.Contains(clean, "..") {
			http.Error(w, "路径无效", http.StatusBadRequest)
			return
		}
		name := strings.TrimPrefix(clean, "/")
		if name == "" {
			name = "index.html"
		}

		// 检查文件是否存在
		file, err := fsys.Open(name)
		if err == nil {
			defer file.Close()
			if info, statErr := file.Stat(); statErr == nil && !info.IsDir() {
				if strings.HasSuffix(name, ".html") || name == "index.html" {
					w.Header().Set("Cache-Control", "no-cache")
				} else if hashedAsset(name) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				ctype := mimeByExt(name)
				w.Header().Set("Content-Type", ctype)
				// 文本资源: 客户端接受 gzip 且体积足够时压缩传输。
				// 压缩结果按文件名缓存(嵌入资源不可变, 一次压缩多次复用)。
				// HEAD 走同一路径计算头部(响应头须与 GET 一致), 只是不写响应体。
				if compressibleType(ctype) && info.Size() >= gzipMinBytes && acceptsGzip(r.Header.Get("Accept-Encoding")) {
					data, readErr := io.ReadAll(file)
					if readErr == nil {
						compressed := gz.compressed(name, data)
						w.Header().Set("Content-Encoding", "gzip")
						w.Header().Set("Vary", "Accept-Encoding")
						w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
						// 显式写状态,避免继承外层(如 gin NoRoute)预设的状态码
						w.WriteHeader(http.StatusOK)
						if r.Method != http.MethodHead {
							_, _ = w.Write(compressed)
						}
						return
					}
					// 读取失败: 回退未压缩传输(文件句柄已消耗, 重新打开)。
					file.Close()
					file, err = fsys.Open(name)
					if err != nil {
						http.Error(w, "读取资源失败", http.StatusInternalServerError)
						return
					}
					defer file.Close()
				}
				// 显式写状态,避免继承外层(如 gin NoRoute)预设的状态码
				w.WriteHeader(http.StatusOK)
				if r.Method == http.MethodHead {
					return
				}
				_, _ = io.Copy(w, file)
				return
			}
			// 目录:落到 SPA fallback
		}

		// 路径含文件扩展名 → 404(不 fallback)
		if path.Ext(clean) != "" && clean != "/" && !strings.HasSuffix(clean, "/") {
			http.NotFound(w, r)
			return
		}

		// SPA fallback:返回 index.html(不存在时 503 中文提示)
		index, err := fsys.Open("index.html")
		if err != nil {
			msg := "管理界面资源未构建,请先运行 npm run build 后重新编译服务"
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, msg)
			return
		}
		defer index.Close()
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 显式写状态,避免继承外层(如 gin NoRoute)预设的状态码
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.Copy(w, index)
	})
}
