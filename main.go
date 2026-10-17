package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	runtimePort      int
	runtimeLANAccess bool
	appRestartCh     chan struct{}
	serverStartTime  time.Time
)

func setRuntimeState(port int, lanAccess bool) {
	runtimePort = port
	runtimeLANAccess = lanAccess
}

func setRestartChannel(ch chan struct{}) {
	appRestartCh = ch
}

func main() {
	portFlag := flag.Int("port", 0, "listening port (overrides config)")
	flag.Bool("no-gui", false, "do not open GUI window (已弃用，GUI 始终开启)")
	flag.Parse()

	serverStartTime = time.Now()

	var safeCfg *SafeConfig
	shutdownCh := make(chan struct{})

	for {
		cfg, err := LoadConfig()
		if err != nil {
			log.Printf("[config] failed to load: %v, using defaults", err)
			cfg = DefaultConfig()
		}

		if *portFlag > 0 {
			cfg.Port = *portFlag
		}
		if cfg.Port <= 0 || cfg.Port > 65535 {
			cfg.Port = 8188
		}

		if safeCfg == nil {
			safeCfg = NewSafeConfig(cfg)
		} else {
			safeCfg.Update(func(c *Config) {
				*c = cfg
			})
		}

		c := safeCfg.Get()

		initTrace()
		logger := NewLogger(2000)
		svc := InitAnalysisService(safeCfg, logger)
		if svc != nil {
			svc.config = safeCfg
		}
		_ = os.Remove("test/proxy_debug_logs.jsonl") // 重启时清空旧的 debug 代理日志
		proxy := NewProxyServer(safeCfg, logger, svc)
		initWSHub(logger)

		// 决定监听的主机地址
		bindHost := "127.0.0.1"
		if c.LANAccess {
			bindHost = "0.0.0.0"
		}
		addr := fmt.Sprintf("%s:%d", bindHost, c.Port)

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("[server] startup failed: failed to listen on %s: %v", addr, err)
		}

		server := &http.Server{
			Addr:    addr,
			Handler: proxy,
		}

		// 异步启动 HTTP 服务
		go func() {
			log.Printf("[server] DSPlus starting on %s", addr)
			if len(c.Providers) > 0 {
				for _, p := range c.Providers {
					log.Printf("[server] provider %q upstream: %s", p.Name, p.BaseURL)
				}
				log.Printf("[server] active provider: %s", c.ActiveProvider)
			} else {
				log.Printf("[server] OpenAI upstream: %s", c.OpenAIUpstream)
				log.Printf("[server] Anthropic upstream: %s", c.AnthropicUpstream)
			}
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Fatalf("[server] failed to start: %v", err)
			}
		}()

		// 异步打开 GUI（GUI 强制开启，不再支持关闭或禁用）
		if !hasGUI() {
			go openGUI(fmt.Sprintf("http://127.0.0.1:%d", c.Port), shutdownCh)
		} else {
			navigateGUI(fmt.Sprintf("http://127.0.0.1:%d", c.Port))
		}

		fmt.Printf("DSPlus v0.2.5\n")
		fmt.Printf("Listening on http://%s:%d\n", bindHost, c.Port)
		fmt.Printf("GUI: http://127.0.0.1:%d\n", c.Port)
		fmt.Printf("GUI 强制开启。窗口关闭按钮（X）会最小化到托盘；使用托盘菜单“退出”或 Ctrl+C 停止服务\n")

		setRuntimeState(c.Port, c.LANAccess)

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		restartCh := make(chan struct{}, 1)
		setRestartChannel(restartCh)

		select {
		case <-sigCh:
			log.Println("[server] shutting down via signal...")
			server.Shutdown(context.Background())
			return
		case <-shutdownCh:
			log.Println("[server] shutting down via GUI close...")
			server.Shutdown(context.Background())
			return
		case <-restartCh:
			log.Println("[server] restarting service...")
			server.Shutdown(context.Background())
			// 给一些时间端口释放
			time.Sleep(300 * time.Millisecond)
		}
	}
}
