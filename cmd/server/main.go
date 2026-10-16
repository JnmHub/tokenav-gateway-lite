package main

//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/setup"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
)

//go:embed VERSION
var embeddedVersion string

// Build-time variables (can be set by ldflags)
var (
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// initLogger configures the default slog handler based on gin.Mode().
// In non-release mode, Debug level logs are enabled.
func main() {
	// Bootstrap logs must not depend on the host OS timezone. The configured
	// timezone is applied again after config loading, but China/Shanghai is the
	// product default and keeps early startup messages consistent as well.
	if err := applyRuntimeTimezone("Asia/Shanghai"); err != nil {
		log.Fatalf("Failed to initialize default timezone: %v", err)
	}
	logger.InitBootstrap()
	defer logger.Sync()

	// Parse command line flags
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		log.Printf("tokenav gateway %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	// CLI setup mode
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// gateway-lite（轻量网关）作为区域运行时使用，不能因为本地缺少安装锁文件就进入 setup wizard（安装向导）。
	if shouldRunSetupWizard(resolveRunModeForSetup(), setup.NeedsSetup()) {
		// Check if auto-setup is enabled (for Docker deployment)
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// Continue to main server after auto-setup
		} else {
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// Normal server mode
	runMainServer()
}

func shouldRunSetupWizard(runMode string, needsSetup bool) bool {
	if config.NormalizeRunMode(runMode) == config.RunModeGatewayLite {
		return false
	}
	return needsSetup
}

func resolveRunModeForSetup() string {
	if runMode := strings.TrimSpace(os.Getenv("RUN_MODE")); runMode != "" {
		return runMode
	}
	cfg, err := config.LoadForBootstrap()
	if err != nil || cfg == nil {
		return config.RunModeGatewayLite
	}
	return cfg.RunMode
}

func runSetupServer() {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	// Register setup routes
	setup.RegisterRoutes(r)

	// Serve embedded frontend if available
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Get server address from config.yaml or environment variables (SERVER_HOST, SERVER_PORT)
	// This allows users to run setup on a different address if needed
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure tokenav gateway")

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

func runMainServer() {
	gatewaylite.EnableBindingRestart(true)
	defer gatewaylite.EnableBindingRestart(false)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	for runMainGeneration(quit) {
	}
}

// A binding change recreates the application only after the old generation's
// HTTP requests and business workers have joined. No os.Exit bypasses cleanup.
func runMainGeneration(quit <-chan os.Signal) bool {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := applyRuntimeTimezone(cfg.Timezone); err != nil {
		log.Fatalf("Failed to initialize configured timezone: %v", err)
	}
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	resetBindingGenerationHealth()
	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	cleanup := onceCleanup(app.Cleanup)
	defer cleanup()

	created, reason, err := setup.EnsureDefaultAdminFromRuntimeConfig(
		cfg,
		cfg.Admin.Email,
		cfg.Admin.Password,
	)
	if err != nil {
		log.Fatalf("Failed to ensure default admin: %v", err)
	}
	if created {
		log.Printf("Default admin created: %s", cfg.Admin.Email)
	} else {
		log.Printf("Default admin bootstrap skipped: %s", reason)
	}

	// 启动服务器
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	restart := false
	select {
	case <-quit:
	case <-gatewaylite.BindingRestartRequests():
		restart = true
	}
	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := shutdownBindingGeneration(ctx, app.Server, cleanup, joinBindingGeneration); err != nil {
		log.Printf("Server shutdown incomplete: %v", err)
		// Never initialize another destination beside requests that failed to join.
		restart = false
	}
	if restart {
		joinCtx, joinCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := gatewaylite.StopBindingWorkers(joinCtx); err != nil {
			log.Printf("Binding restart deferred: old workers did not stop")
			restart = false
		}
		joinCancel()
	}
	if !bindingGenerationReadyToRestart() {
		restart = false
	}
	log.Println("Server exited")
	return restart
}

func applyRuntimeTimezone(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return err
	}
	time.Local = location
	return nil
}

// onceCleanup 让显式关停路径和兜底 defer 可以安全共用同一个清理函数；
// 用量缓冲和计费队列只能刷出一次，否则可能产生重复回传。
func onceCleanup(cleanup func()) func() {
	var once sync.Once
	return func() {
		once.Do(cleanup)
	}
}

// shutdownAndCleanup 保证 HTTP 关停无论成功、超时还是报错，都先执行一次业务清理再返回。
func shutdownAndCleanup(ctx context.Context, shutdown func(context.Context) error, cleanup func()) error {
	err := shutdown(ctx)
	cleanup()
	return err
}
