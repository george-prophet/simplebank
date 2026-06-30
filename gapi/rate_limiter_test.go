package gapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/techschool/simplebank/util"
)

// gatewayRoutes mimics the HTTP gateway surface: the four /v1 endpoints all
// return 200 so we can isolate the rate limiter's behavior from the handlers.
func gatewayRoutes() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{
		"/v1/create_user",
		"/v1/login_user",
		"/v1/update_user",
		"/v1/verify_email",
	} {
		mux.HandleFunc(path, func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusOK)
		})
	}
	return mux
}

func rateLimitConfig(requests int64, window time.Duration) util.Config {
	return util.Config{
		RateLimitEnabled:  true,
		RateLimitRequests: requests,
		RateLimitWindow:   window,
	}
}

// newRequest builds a request to a gateway path from a given client IP:port.
func newRequest(method, path, remoteAddr string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	return req
}

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	handler := NewRateLimiter(rateLimitConfig(5, time.Minute), gatewayRoutes())

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.1:1234"))
		require.Equal(t, http.StatusOK, rec.Code, "request %d should be allowed", i+1)
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	const limit = 5
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())

	var lastRec *httptest.ResponseRecorder
	for i := 0; i < limit+1; i++ {
		lastRec = httptest.NewRecorder()
		handler.ServeHTTP(lastRec, newRequest(http.MethodPost, "/v1/create_user", "10.0.0.2:5555"))
	}

	require.Equal(t, http.StatusTooManyRequests, lastRec.Code)
	require.Equal(t, "application/json", lastRec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"error":"rate limit exceeded, please retry later"}`, lastRec.Body.String())
	require.NotEmpty(t, lastRec.Header().Get("Retry-After"))
	require.Equal(t, "0", lastRec.Header().Get("X-RateLimit-Remaining"))
}

func TestRateLimiterIsPerClientIP(t *testing.T) {
	const limit = 3
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())

	// Exhaust the first client's budget.
	for i := 0; i < limit+1; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.3:1111"))
	}

	// A different IP must still be served from a fresh bucket.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.4:2222"))
	require.Equal(t, http.StatusOK, rec.Code)
}

// The same client across different ports is one logical client: the key is the
// IP only, so the port must not create separate buckets.
func TestRateLimiterIgnoresClientPort(t *testing.T) {
	const limit = 2
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())

	ports := []string{"10.0.0.5:1000", "10.0.0.5:2000", "10.0.0.5:3000"}
	codes := make([]int, len(ports))
	for i, addr := range ports {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
		codes[i] = rec.Code
	}

	require.Equal(t, http.StatusOK, codes[0])
	require.Equal(t, http.StatusOK, codes[1])
	require.Equal(t, http.StatusTooManyRequests, codes[2])
}

func TestRateLimiterSetsHeadersWhenAllowed(t *testing.T) {
	handler := NewRateLimiter(rateLimitConfig(10, time.Minute), gatewayRoutes())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.6:1234"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "10", rec.Header().Get("X-RateLimit-Limit"))
	require.Equal(t, "9", rec.Header().Get("X-RateLimit-Remaining"))
	require.NotEmpty(t, rec.Header().Get("X-RateLimit-Reset"))
}

// When disabled, the middleware is a transparent pass-through: no limiting, no
// rate-limit headers.
func TestRateLimiterDisabledPassesThrough(t *testing.T) {
	config := util.Config{RateLimitEnabled: false}
	handler := NewRateLimiter(config, gatewayRoutes())

	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.7:1234"))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, rec.Header().Get("X-RateLimit-Limit"))
	}
}

// TestRateLimiterSequence mirrors firing a burst of requests from one client:
// exactly the first `limit` succeed and every request beyond it gets 429. This
// is the programmatic version of the browser-console loop.
func TestRateLimiterSequence(t *testing.T) {
	const (
		limit = 20
		burst = 25
	)
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())

	var allowed, limited int
	for i := 0; i < burst; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", "10.0.0.9:1234"))
		switch rec.Code {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("request %d: unexpected status %d", i+1, rec.Code)
		}
	}

	require.Equal(t, limit, allowed, "exactly the limit should be allowed")
	require.Equal(t, burst-limit, limited, "the remainder should be limited")
}

// TestRateLimiterResetsAfterWindow exhausts the budget, then waits for the
// fixed window to roll over and confirms the bucket refills. This captures the
// behavior behind seeing all-429s on a repeat run within the same window.
func TestRateLimiterResetsAfterWindow(t *testing.T) {
	const (
		limit  = 2
		window = 100 * time.Millisecond
	)
	handler := NewRateLimiter(rateLimitConfig(limit, window), gatewayRoutes())
	const addr = "10.0.0.10:1234"

	// Drain the budget for this window.
	for i := 0; i < limit; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	// Next request is over the limit.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	// After the window elapses, the bucket refills and requests succeed again.
	time.Sleep(window + 50*time.Millisecond)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestRateLimiterBoundaryBurst documents a known property of the fixed-window
// algorithm: because the budget resets at the window boundary rather than
// sliding, a client can spend its full budget at the tail of one window and its
// full budget again at the head of the next. The result is up to 2*limit
// requests served in a span much shorter than the window. This is the
// "boundary burst"; switching to a sliding-window/Redis store is the fix if it
// matters. The test pins the behavior so a future change to the algorithm is a
// deliberate, visible decision rather than a silent regression.
func TestRateLimiterBoundaryBurst(t *testing.T) {
	const (
		limit  = 5
		window = 100 * time.Millisecond
	)
	handler := NewRateLimiter(rateLimitConfig(limit, window), gatewayRoutes())
	const addr = "10.0.0.11:1234"

	allowed := func() int {
		n := 0
		for i := 0; i < limit; i++ {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
			if rec.Code == http.StatusOK {
				n++
			}
		}
		return n
	}

	// Spend the full budget at the tail of the first window.
	first := allowed()
	require.Equal(t, limit, first)

	// Cross the boundary and spend the full budget again at the head of the next.
	time.Sleep(window + 50*time.Millisecond)
	second := allowed()
	require.Equal(t, limit, second)

	// 2*limit requests were served across the boundary, double the nominal rate.
	require.Equal(t, 2*limit, first+second)
}

// TestRateLimiterLargeLimitNoOverflow guards the int64 path end to end: a limit
// above 2^32 is honored exactly, with no 32-bit truncation in the counter, the
// remaining computation, or the X-RateLimit-* headers.
func TestRateLimiterLargeLimitNoOverflow(t *testing.T) {
	const limit = int64(1) << 33 // 8,589,934,592 — well past 2^32.
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())
	const addr = "10.0.0.12:1234"

	for i := int64(1); i <= 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "8589934592", rec.Header().Get("X-RateLimit-Limit"))
		// Remaining = limit - count, with no wraparound.
		require.Equal(t, strconv.FormatInt(limit-i, 10), rec.Header().Get("X-RateLimit-Remaining"))
	}
}

// TestRateLimiterConcurrentRequestsAreAtomic fires 2*limit requests from one IP
// concurrently and asserts exactly `limit` are allowed. This proves the
// underlying counter increments atomically — a racy implementation would let
// more than `limit` through under contention.
func TestRateLimiterConcurrentRequestsAreAtomic(t *testing.T) {
	const limit = 50
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())
	const addr = "10.0.0.13:1234"

	var allowed, limited int64
	var wg sync.WaitGroup
	for i := 0; i < 2*limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/login_user", addr))
			switch rec.Code {
			case http.StatusOK:
				atomic.AddInt64(&allowed, 1)
			case http.StatusTooManyRequests:
				atomic.AddInt64(&limited, 1)
			}
		}()
	}
	wg.Wait()

	require.Equal(t, int64(limit), allowed, "exactly the limit should be allowed under concurrency")
	require.Equal(t, int64(limit), limited)
}

// TestRateLimiterSharesBudgetAcrossPaths confirms the bucket is keyed on client
// IP alone: requests to different gateway routes draw from the same budget, so
// a client can't multiply its allowance by spreading traffic across endpoints.
func TestRateLimiterSharesBudgetAcrossPaths(t *testing.T) {
	const limit = 3
	handler := NewRateLimiter(rateLimitConfig(limit, time.Minute), gatewayRoutes())
	const addr = "10.0.0.14:1234"

	// One request each to three distinct paths exhausts the shared budget.
	for _, path := range []string{"/v1/create_user", "/v1/login_user", "/v1/update_user"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(http.MethodPost, path, addr))
		require.Equal(t, http.StatusOK, rec.Code, "path %s should be allowed", path)
	}

	// A fourth request to yet another path is limited — the budget is per-IP.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/verify_email", addr))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestClientIP(t *testing.T) {
	require.Equal(t, "10.0.0.8", clientIP(newRequest(http.MethodGet, "/v1/login_user", "10.0.0.8:4567")))
	// IPv6 peer: SplitHostPort strips the brackets and the port.
	require.Equal(t, "::1", clientIP(newRequest(http.MethodGet, "/v1/login_user", "[::1]:4567")))
	// No port present: returned verbatim rather than dropped.
	req := httptest.NewRequest(http.MethodGet, "/v1/login_user", nil)
	req.RemoteAddr = "malformed-no-port"
	require.Equal(t, "malformed-no-port", clientIP(req))
}
