package gapi

import (
	"net/http"
	"net/http/httptest"
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

func TestClientIP(t *testing.T) {
	require.Equal(t, "10.0.0.8", clientIP(newRequest(http.MethodGet, "/v1/login_user", "10.0.0.8:4567")))
	// No port present: returned verbatim rather than dropped.
	req := httptest.NewRequest(http.MethodGet, "/v1/login_user", nil)
	req.RemoteAddr = "malformed-no-port"
	require.Equal(t, "malformed-no-port", clientIP(req))
}
