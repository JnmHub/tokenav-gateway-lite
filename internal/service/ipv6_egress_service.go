package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// IPv6EgressEntry 单个本机 IPv6 出口的扫描与测通状态。
type IPv6EgressEntry struct {
	Address   string    `json:"address"`
	Status    string    `json:"status"` // ok / failed
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	ProxyID   int64     `json:"proxy_id,omitempty"`
	ProxyURL  string    `json:"proxy_url,omitempty"`
	LastCheck time.Time `json:"last_check"`
}

// IPv6EgressService 本机 IPv6 出口管理器：
//  1. 扫描网卡全局 IPv6（排除 link-local/回环）；
//  2. 每个地址以源绑定方式对配置目标（默认 chatgpt.com:443）做真实 TLS 握手测通；
//  3. 测通地址自动注册为本地 HTTP CONNECT 代理——网关只开一个监听端口，
//     代理 URL 的 username 字段携带 IPv6 地址（http://[v6]@127.0.0.1:port），
//     CONNECT 服务端按 username 选择出站源地址；
//  4. 注册进 proxy 表后即可被账号代理池使用，自动复用最小在途调度与
//     每代理并发/最近状态观测。
//
// 配置来源优先级：运行时管理接口写入的持久化文件（data/ipv6_egress.json，
// 热更新、免重启）> config.yaml（gateway.ipv6_egress 段）。
// 仅独立/standard 部署可写本地 proxy 表；gateway-lite 快照模式下自动注册
// 不会生效（代理配置来自主站下发）。
type IPv6EgressService struct {
	cfg          config.GatewayIPv6EgressConfig
	proxyService *ProxyService
	statePath    string

	mu       sync.RWMutex
	entries  map[string]*IPv6EgressEntry
	lastScan time.Time
	started  bool
	listener net.Listener
	scanCtx  context.Context
	scanCancel context.CancelFunc
}

// IPv6EgressStateFile 持久化运行时配置的默认路径（相对网关工作目录）。
const IPv6EgressStateFile = "data/ipv6_egress.json"

// NewIPv6EgressService 创建 IPv6 出口管理器（未启用时各方法为安全空操作）。
// statePath 为空时用默认 data/ipv6_egress.json；持久化配置存在时覆盖 yaml 值。
func NewIPv6EgressService(cfg *config.Config, proxyService *ProxyService) *IPv6EgressService {
	s := &IPv6EgressService{entries: make(map[string]*IPv6EgressEntry), statePath: IPv6EgressStateFile}
	if cfg != nil {
		s.cfg = cfg.Gateway.IPv6Egress
	}
	s.proxyService = proxyService
	s.normalizeConfig(&s.cfg)
	// 持久化的运行时配置优先于 yaml：管理界面保存过即以界面值为准。
	// 覆盖后必须再补全一次默认值——文件可能来自旧版本（字段缺失/为空），
	// 避免空 listen_port 等脏值直接生效。
	if persisted := s.loadPersistedConfig(); persisted != nil {
		s.cfg = *persisted
		s.normalizeConfig(&s.cfg)
	}
	return s
}

func (s *IPv6EgressService) normalizeConfig(cfg *config.GatewayIPv6EgressConfig) {
	if cfg.ListenHost == "" {
		cfg.ListenHost = "127.0.0.1"
	}
	if cfg.ListenPort <= 0 {
		cfg.ListenPort = 32600
	}
	if strings.TrimSpace(cfg.ProbeTarget) == "" {
		cfg.ProbeTarget = "chatgpt.com:443"
	}
	if cfg.SyncIntervalSeconds <= 0 {
		cfg.SyncIntervalSeconds = 300
	}
	if strings.TrimSpace(cfg.NamePrefix) == "" {
		cfg.NamePrefix = "ipv6"
	}
	if cfg.MaxAddresses <= 0 {
		cfg.MaxAddresses = 512
	}
}

// loadPersistedConfig 读取管理界面持久化的运行时配置；无文件或损坏时返回 nil。
func (s *IPv6EgressService) loadPersistedConfig() *config.GatewayIPv6EgressConfig {
	if s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return nil
	}
	var cfg config.GatewayIPv6EgressConfig
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	return &cfg
}

func (s *IPv6EgressService) savePersistedConfig(cfg config.GatewayIPv6EgressConfig) error {
	if s.statePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.statePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(s.statePath, data, 0o644)
}

// RuntimeConfig 返回当前生效配置（含持久化覆盖后的值）。
func (s *IPv6EgressService) RuntimeConfig() config.GatewayIPv6EgressConfig {
	if s == nil {
		return config.GatewayIPv6EgressConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// ApplyRuntimeConfig 热更新配置（管理界面调用，无需重启进程）：
// 校验并补全默认值 → 持久化到 data/ipv6_egress.json → 停止旧监听/扫描循环 →
// 按新配置重启。关闭功能只停不启。
func (s *IPv6EgressService) ApplyRuntimeConfig(next config.GatewayIPv6EgressConfig) error {
	if s == nil {
		return errors.New("ipv6 egress service unavailable")
	}
	if next.ListenPort < 0 || next.ListenPort > 65535 {
		return errors.New("listen_port 必须在 0-65535 之间")
	}
	if next.SyncIntervalSeconds < 0 {
		return errors.New("sync_interval_seconds 不能为负")
	}
	if next.MaxAddresses < 0 {
		return errors.New("max_addresses 不能为负")
	}
	// 测通目标可空（用默认），非空时必须 host[:port] 合法。
	if target := strings.TrimSpace(next.ProbeTarget); target != "" {
		if _, _, err := net.SplitHostPort(withDefaultPort(target)); err != nil {
			return fmt.Errorf("probe_target 格式非法（应为 host:port）: %w", err)
		}
	}
	s.normalizeConfig(&next)

	if err := s.savePersistedConfig(next); err != nil {
		return fmt.Errorf("持久化配置失败: %w", err)
	}

	// 持锁完成"停止旧组件 + 切换配置"，避免与 Start/Stop 并发竞争。
	s.mu.Lock()
	s.stopLocked()
	s.cfg = next
	s.mu.Unlock()
	if next.Enabled {
		s.Start(context.Background())
	}
	return nil
}

func withDefaultPort(target string) string {
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	return target + ":443"
}

// Start 启用出口管理：监听本地 CONNECT 代理并开启周期扫描（先做一次同步扫描）。
func (s *IPv6EgressService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.cfg.Enabled || s.started {
		s.mu.Unlock()
		return
	}
	addr := net.JoinHostPort(s.cfg.ListenHost, strconv.Itoa(s.cfg.ListenPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		slog.Error("ipv6_egress_listen_failed", "addr", addr, "error", err)
		return
	}
	scanCtx, scanCancel := context.WithCancel(ctx)
	s.started = true
	s.listener = ln
	s.scanCtx = scanCtx
	s.scanCancel = scanCancel
	s.mu.Unlock()

	slog.Info("ipv6_egress_started", "listen", addr, "probe_target", s.cfg.ProbeTarget)
	go s.acceptLoop(ln)
	go s.scanLoop(scanCtx)
}

// Stop 停止监听与扫描循环（幂等，可再次 Start）。
func (s *IPv6EgressService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
}

// stopLocked 停止运行组件（需持有写锁）。
func (s *IPv6EgressService) stopLocked() {
	if s.scanCancel != nil {
		s.scanCancel()
		s.scanCancel = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	s.scanCtx = nil
	s.started = false
}

func (s *IPv6EgressService) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serveCONNECT(conn)
	}
}

// serveCONNECT 处理一条 HTTP CONNECT 隧道：
// 代理认证 username 携带出站 IPv6 源地址，校验该地址已测通后建立隧道。
func (s *IPv6EgressService) serveCONNECT(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = conn.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
		return
	}
	sourceIP := ""
	if user := req.URL.User; user != nil {
		sourceIP = strings.Trim(user.Username(), "[]")
	}
	if sourceIP == "" && req.Header.Get("Proxy-Authorization") != "" {
		if u, _, ok := parseProxyBasicAuth(req.Header.Get("Proxy-Authorization")); ok {
			sourceIP = strings.Trim(u, "[]")
		}
	}
	if parsed := net.ParseIP(sourceIP); parsed == nil || !s.allowedSource(parsed.String()) {
		// 未登记（或测通失败）的地址不允许作为出口，避免任意源绑定。
		_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\n\r\n"))
		return
	}

	dialer := &net.Dialer{Timeout: 15 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceIP)}}
	target := req.URL.Host
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "443")
	}
	upstream, err := dialer.Dial("tcp", target)
	if err != nil {
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer func() { _ = upstream.Close() }()
	_ = conn.SetDeadline(time.Time{})
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	relay(conn, upstream)
}

func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyFunc := func(dst, src net.Conn) {
		_, _ = copyConn(dst, src)
		done <- struct{}{}
	}
	go copyFunc(a, b)
	go copyFunc(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
}

func copyConn(dst, src net.Conn) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err != nil {
			return total, nil
		}
	}
}

func parseProxyBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	i := strings.IndexByte(string(raw), ':')
	if i < 0 {
		return string(raw), "", true
	}
	return string(raw[:i]), string(raw[i+1:]), true
}

// allowedSource 判定地址是否已登记且测通（CONNECT 出站源白名单）。
func (s *IPv6EgressService) allowedSource(address string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[address]
	return ok && e != nil && e.Status == "ok"
}

func (s *IPv6EgressService) scanLoop(ctx context.Context) {
	interval := time.Duration(s.cfg.SyncIntervalSeconds) * time.Second
	for {
		s.Rescan(context.WithoutCancel(ctx))
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Rescan 执行一次"识别 → 测通 → 注册"全流程（也供手动触发）。
func (s *IPv6EgressService) Rescan(ctx context.Context) {
	if s == nil || !s.cfg.Enabled {
		return
	}
	addresses := ScanGlobalIPv6(s.cfg.MaxAddresses)
	results := make(map[string]*IPv6EgressEntry, len(addresses))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, addr := range addresses {
		wg.Add(1)
		go func(ip net.IP) {
			defer wg.Done()
			entry := probeIPv6Egress(ip, s.cfg.ProbeTarget)
			mu.Lock()
			results[entry.Address] = entry
			mu.Unlock()
		}(addr)
	}
	wg.Wait()

	s.mu.Lock()
	s.entries = results
	s.lastScan = time.Now()
	entries := make([]IPv6EgressEntry, 0, len(results))
	for _, e := range results {
		entries = append(entries, *e)
	}
	s.mu.Unlock()

	s.syncProxies(ctx, entries)
	okCount := 0
	for _, e := range entries {
		if e.Status == "ok" {
			okCount++
		}
	}
	slog.Info("ipv6_egress_scan_done", "found", len(entries), "ok", okCount)
}

// Running 返回监听与扫描循环是否在运行。
func (s *IPv6EgressService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started
}

// Status 返回管理器状态快照（admin 展示用）。
func (s *IPv6EgressService) Status() (enabled bool, lastScan time.Time, entries []IPv6EgressEntry) {
	if s == nil {
		return false, time.Time{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries = make([]IPv6EgressEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Address < entries[j].Address })
	return s.cfg.Enabled, s.lastScan, entries
}

// syncProxies 维护 proxy 表与测通结果的一致性：
//  1. 测通地址不存在则注册（幂等：host+port+username 唯一判定）；
//  2. 本管理器前缀注册、但已不再测通（地址消失/握手失败）的代理置为
//     inactive——账号池快照仍可引用，但列表可见其停用状态，管理员可
//     决定清理；避免失效出口被持续调度与 proxy 表无限堆积。
func (s *IPv6EgressService) syncProxies(ctx context.Context, entries []IPv6EgressEntry) {
	if s.proxyService == nil {
		return
	}
	okAddresses := make(map[string]struct{}, len(entries))
	for i := range entries {
		e := &entries[i]
		if e.Status != "ok" {
			continue
		}
		okAddresses[e.Address] = struct{}{}
		exists, err := s.proxyService.proxyRepo.ExistsByHostPortAuth(ctx, s.cfg.ListenHost, s.cfg.ListenPort, e.Address, "")
		if err != nil {
			slog.Warn("ipv6_egress_proxy_exists_check_failed", "address", e.Address, "error", err)
			continue
		}
		if exists {
			continue
		}
		created, err := s.proxyService.Create(ctx, CreateProxyRequest{
			Name:     fmt.Sprintf("%s-%s", s.cfg.NamePrefix, e.Address),
			Protocol: "http",
			Host:     s.cfg.ListenHost,
			Port:     s.cfg.ListenPort,
			Username: e.Address,
		})
		if err != nil {
			slog.Warn("ipv6_egress_proxy_create_failed", "address", e.Address, "error", err)
			continue
		}
		e.ProxyID = created.ID
	}

	// 停用不再测通的本前缀代理：按"监听地址+端口"圈定本管理器注册的记录，
	// username 即 IPv6 地址；仍测通的跳过。
	inactive := "inactive"
	owned, _, err := s.proxyService.proxyRepo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 1000}, "", "", "")
	if err != nil {
		slog.Warn("ipv6_egress_proxy_list_for_cleanup_failed", "error", err)
		return
	}
	for _, proxy := range owned {
		if proxy.Host != s.cfg.ListenHost || proxy.Port != s.cfg.ListenPort {
			continue
		}
		if !strings.HasPrefix(proxy.Name, s.cfg.NamePrefix+"-") || proxy.Username == "" {
			continue
		}
		if proxy.Status != StatusActive {
			continue // 已停用的恢复逻辑在下方单独处理。
		}
		if _, stillOK := okAddresses[proxy.Username]; stillOK {
			continue
		}
		// 地址消失或测通失败：置 inactive，避免失效出口被持续调度。
		if _, err := s.proxyService.Update(ctx, proxy.ID, UpdateProxyRequest{Status: &inactive}); err != nil {
			slog.Warn("ipv6_egress_proxy_deactivate_failed", "proxy_id", proxy.ID, "error", err)
		} else {
			slog.Info("ipv6_egress_proxy_deactivated", "proxy_id", proxy.ID, "address", proxy.Username)
		}
	}
	// 恢复分支：本轮重新测通、此前被停用的地址 → 置回 active。
	active := StatusActive
	for address := range okAddresses {
		for _, proxy := range owned {
			if proxy.Host == s.cfg.ListenHost && proxy.Port == s.cfg.ListenPort &&
				proxy.Username == address && proxy.Status == inactive &&
				strings.HasPrefix(proxy.Name, s.cfg.NamePrefix+"-") {
				if _, err := s.proxyService.Update(ctx, proxy.ID, UpdateProxyRequest{Status: &active}); err != nil {
					slog.Warn("ipv6_egress_proxy_reactivate_failed", "proxy_id", proxy.ID, "error", err)
				}
			}
		}
	}
}

// ScanGlobalIPv6 枚举本机所有全局单播 IPv6（排除 link-local、回环、IPv4）。
func ScanGlobalIPv6(max int) []net.IP {
	if max <= 0 {
		max = 512
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]net.IP, 0, 16)
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() != nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() || !ip.IsGlobalUnicast() {
				continue
			}
			key := ip.String()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ip)
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}

// probeIPv6Egress 以指定 IPv6 为源地址对目标做真实 TCP+TLS 握手测通。
func probeIPv6Egress(sourceIP net.IP, target string) *IPv6EgressEntry {
	entry := &IPv6EgressEntry{Address: sourceIP.String(), Status: "failed", LastCheck: time.Now()}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
		target = net.JoinHostPort(target, "443")
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, LocalAddr: &net.TCPAddr{IP: sourceIP}}
	start := time.Now()
	conn, err := tls.DialWithDialer(dialer, "tcp", target, &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
	})
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	_ = conn.Close()
	entry.Status = "ok"
	entry.LatencyMS = time.Since(start).Milliseconds()
	return entry
}
