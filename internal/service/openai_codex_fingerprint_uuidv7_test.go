package service

import (
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// uuidV7Pattern 校验 UUID 字符串的 version 位是 7（时间有序），
// 对应官方 Codex CLI session_id/thread_id 的格式。
var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// convergedCodexSessionIDs 的会话语义：会话内稳定、TTL 轮换、时间戳为近期真实时间。
func TestConvergedCodexSessionIDs_SessionSemantics(t *testing.T) {
	resetCodexFingerprintSessions()
	defer resetCodexFingerprintSessions()

	now := time.Now()
	first, firstThread := convergedCodexSessionIDs(7, now)

	if !uuidV7Pattern.MatchString(first) {
		t.Fatalf("session_id 应为 UUIDv7 格式: %s", first)
	}
	// 官方取证：session-id == thread-id。
	if firstThread != first {
		t.Fatalf("thread_id 应与 session_id 同值（官方取证语义）: %s != %s", firstThread, first)
	}
	// 会话内稳定：同账号同窗口内再次取值不变。
	again, _ := convergedCodexSessionIDs(7, now.Add(time.Minute))
	if again != first {
		t.Fatalf("同一会话内 session_id 必须稳定: %s != %s", again, first)
	}
	// 不同账号是不同会话。
	other, _ := convergedCodexSessionIDs(8, now)
	if other == first {
		t.Fatal("不同账号不应共享会话")
	}
	// 时间戳是近期真实时间：v7 前 48 位是毫秒时间戳，应在 now 附近 10 分钟内。
	parsed := uuid.MustParse(first)
	ts := uuidMillis(parsed)
	if ts < now.Add(-10*time.Minute).UnixMilli() || ts > now.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("session_id 时间戳应接近真实生成时间: %d vs now %d", ts, now.UnixMilli())
	}
	// TTL 过期后轮换出新会话。注意：新 v7 由真实墙钟生成，因此其时间戳
	// 只需晚于原会话生成时刻（不早于原 now），且必然是"近期"值。
	rotated, _ := convergedCodexSessionIDs(7, now.Add(codexFingerprintSessionTTL+time.Second))
	if rotated == first {
		t.Fatalf("TTL 过期后应轮换新 session_id: %s", rotated)
	}
	parsedRotated := uuid.MustParse(rotated)
	if ts := uuidMillis(parsedRotated); ts < now.UnixMilli() {
		t.Fatalf("轮换后的 session 时间戳不应早于原会话生成时刻: %d", ts)
	}
}

// uuidMillis 从 google/uuid 的 v7 时间位换算 Unix 毫秒（其 Time 为 100ns 单位）。
func uuidMillis(u uuid.UUID) int64 {
	sec, nsec := u.Time().UnixTime()
	return sec*1000 + nsec/1e6
}

// probe 会话 ID 应为真实时间的一次性 UUIDv7（不再确定性派生）。
func TestCompactProbeSessionID_IsFreshUUIDv7(t *testing.T) {
	first := compactProbeSessionID(7)
	second := compactProbeSessionID(7)
	if !uuidV7Pattern.MatchString(first) {
		t.Fatalf("probe session_id 应为 UUIDv7 格式: %s", first)
	}
	if first == second {
		t.Fatal("probe 是一次性会话，两次调用应不同")
	}
}

// 并发正确性：同账号首次并发请求（以及过期轮换竞争）必须收敛到同一个
// 会话 ID——读取-生成-写入的竞态会让上游看到同一瞬间多个不同 session。
func TestConvergedCodexSessionIDs_ConcurrentFirstAccessSingleSession(t *testing.T) {
	resetCodexFingerprintSessions()
	defer resetCodexFingerprintSessions()

	const goroutines = 100
	var wg sync.WaitGroup

	results := make(chan string, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, _ := convergedCodexSessionIDs(101, time.Now())
			results <- id
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	first := ""
	for id := range results {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("并发首次访问产生了不同会话 ID: %s vs %s", first, id)
		}
	}

	// 过期轮换竞争：同样必须收敛到同一个新会话。
	future := time.Now().Add(codexFingerprintSessionTTL + time.Second)
	results2 := make(chan string, goroutines)
	start2 := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start2
			id, _ := convergedCodexSessionIDs(101, future)
			results2 <- id
		}()
	}
	close(start2)
	wg.Wait()
	close(results2)
	rotated := ""
	for id := range results2 {
		if rotated == "" {
			rotated = id
			continue
		}
		if id != rotated {
			t.Fatalf("并发轮换产生了不同会话 ID: %s vs %s", rotated, id)
		}
	}
	if rotated == first {
		t.Fatal("轮换后应产生新会话")
	}
}
