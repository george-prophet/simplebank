package gapi

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/techschool/simplebank/util"
	"github.com/ulule/limiter/v3"
	"github.com/ulule/limiter/v3/drivers/store/memory"
)

// clientIP extracts the client's IP address from the request, stripping the
// port. We deliberately key on RemoteAddr (the real TCP peer) rather than the
// X-Forwarded-For header, which is client-controlled and trivially spoofed.
// If the service is ever placed behind a trusted reverse proxy or load
// balancer, revisit this to honor the proxy's forwarded header instead.
func clientIP(req *http.Request) string {
	ip, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return ip
}

// NewRateLimiter wraps the given handler with a per-client-IP rate limiter,
// backed by an in-memory fixed-window store. When rate limiting is disabled in
// config, the handler is returned unchanged.
func NewRateLimiter(config util.Config, handler http.Handler) http.Handler {
	if !config.RateLimitEnabled {
		return handler
	}

	rate := limiter.Rate{
		Period: config.RateLimitWindow,
		Limit:  config.RateLimitRequests,
	}
	instance := limiter.New(memory.NewStore(), rate)

	return rateLimitHandler(instance, handler)
}

func rateLimitHandler(instance *limiter.Limiter, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		key := clientIP(req)

		ctx, err := instance.Get(req.Context(), key)
		if err != nil {
			// Fail open: a limiter/store failure must not take down the API.
			// Log it and let the request through.
			log.Error().Err(err).Str("client_ip", key).Msg("rate limiter error")
			handler.ServeHTTP(res, req)
			return
		}

		res.Header().Set("X-RateLimit-Limit", strconv.FormatInt(ctx.Limit, 10))
		res.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(ctx.Remaining, 10))
		res.Header().Set("X-RateLimit-Reset", strconv.FormatInt(ctx.Reset, 10))

		if ctx.Reached {
			retryAfter := ctx.Reset - time.Now().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			res.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			res.Header().Set("Content-Type", "application/json")
			res.WriteHeader(http.StatusTooManyRequests)
			res.Write([]byte(`{"error":"rate limit exceeded, please retry later"}`))

			log.Warn().
				Str("client_ip", key).
				Str("path", req.URL.Path).
				Msg("rate limit exceeded")
			return
		}

		handler.ServeHTTP(res, req)
	})
}
