//go:build windows

// PhysicsLab — Go 桌面版。
//
// 原版是单文件 HTML+JS（PhysicsLab.html，18 个交互式数值实验，
// 全部计算在浏览器里完成）。本程序用 Go 把它包成 Windows EXE：
// 程序启动一个只监听 127.0.0.1 的本地服务，把内嵌的页面喂给
// WebView2（Edge 内核）窗口，双击即开，不走默认浏览器、不联网。
// 数值计算仍由原页面里的 JS 完成，Go 只负责装载与窗口，不管物理。
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	appName    = "Physics Lab 3.0"
	windowTitle = "Physics Lab 3.0 · 物理计算与可视化实验室"
	appVersion = "1.0.0"
)

//go:embed PhysicsLab.html
var physicsLabHTML []byte

func main() {
	serverOnly := flag.Bool("server-only", false, "只启动本地服务并打印地址，不开窗口")
	check := flag.Bool("check", false, "自检：启动服务、请求首页与 /health，通过后退出")
	browser := flag.Bool("browser", false, "用默认浏览器打开，而不是桌面窗口")
	flag.Parse()

	url, shutdown, err := startServer()
	if err != nil {
		fatalBox("Physics Lab 启动失败", "本地服务启动失败：\n"+err.Error())
		os.Exit(1)
	}
	defer shutdown()

	switch {
	case *check:
		if err := selfCheck(url); err != nil {
			fmt.Fprintln(os.Stderr, "CHECK FAILED:", err)
			os.Exit(1)
		}
		fmt.Println("CHECK OK:", url)
		return
	case *serverOnly:
		fmt.Println("Physics Lab 本地服务运行中：", url)
		fmt.Println("用浏览器打开上面地址即可使用，Ctrl+C 退出。")
		select {}
	case *browser:
		fmt.Println("正在用默认浏览器打开：", url)
		openBrowser(url)
		select {}
	default:
		runDesktop(url)
	}
}

// startServer 在 127.0.0.1 的随机端口上提供内嵌页面，只本机可访。
func startServer() (string, func(), error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(physicsLabHTML)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/"
	return url, func() { _ = srv.Close() }, nil
}

func selfCheck(url string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, path := range []string{"/health", "/"} {
		resp, err := client.Get(url + strings.TrimPrefix(path, "/"))
		if err != nil {
			return fmt.Errorf("请求 %s 失败：%w", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s 返回 %s", path, resp.Status)
		}
		if path == "/" && !strings.Contains(string(body), "Physics Lab") {
			return fmt.Errorf("首页内容不对，未找到 Physics Lab")
		}
		if path == "/health" && strings.TrimSpace(string(body)) != "ok" {
			return fmt.Errorf("/health 返回异常：%q", string(body))
		}
	}
	return nil
}

// runDesktop 打开 WebView2 桌面窗口。无 WebView2 运行时则弹框说明，
// 用户确认后退回默认浏览器打开（服务继续在后台提供页面）。
func runDesktop(url string) {
	hideConsole()

	dataPath := filepath.Join(userDataDir(), "PhysicsLab", "webview2")
	_ = os.MkdirAll(dataPath, 0o755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:    false,
		AutoFocus: true,
		DataPath: dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  windowTitle,
			Width:  1280,
			Height: 800,
			Center: true,
		},
	})
	if w == nil {
		if msgBox("Physics Lab", "没有检测到 Microsoft Edge WebView2 运行时，无法打开桌面窗口。\n\n点“确定”用默认浏览器打开（需要联网安装 WebView2 才能用桌面窗口，Win11 一般自带）；\n点“取消”退出。", 0x00000001|0x00000030) == 2 { // MB_OKCANCEL | MB_ICONWARNING, IDCANCEL=2
			return
		}
		openBrowser(url)
		select {}
	}
	defer w.Destroy()
	w.SetSize(900, 600, webview2.HintMin)
	w.Navigate(url)
	w.Run()
}

func userDataDir() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return dir
	}
	return os.TempDir()
}

func openBrowser(url string) {
	// rundll32 是系统自带，用默认浏览器打开本地地址。
	_ = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Start()
}

// --- 下面是少量 Win32 调用：隐藏控制台、弹消息框 ---

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW  = user32.NewProc("MessageBoxW")
	procShowWindow   = user32.NewProc("ShowWindow")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsole   = kernel32.NewProc("GetConsoleWindow")
)

// hideConsole 把本进程的控制台窗口藏起来（双击运行时不留黑框）。
// 从命令行启动时（--check 等）不调用本函数，输出正常可见。
func hideConsole() {
	hwnd, _, _ := procGetConsole.Call()
	if hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
}

// msgBox 弹一个系统消息框，返回点击的按钮（1=确定，2=取消）。
func msgBox(title, text string, flags uintptr) uintptr {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return ret
}

func fatalBox(title, text string) {
	msgBox(title, text, 0x00000000|0x00000010) // MB_OK | MB_ICONERROR
}
