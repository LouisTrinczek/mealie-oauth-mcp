package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
)

func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if err, isError := recovered.(error); isError && errors.Is(err, http.ErrAbortHandler) {
					panic(recovered)
				}
				logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func SecurityHeaders(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("Referrer-Policy", "no-referrer")
			header.Set("Cache-Control", "no-store")
			if hsts {
				header.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	remoteIP := remote.Addr().Unmap()
	if !isTrusted(remoteIP, trusted) {
		return remoteIP
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return remoteIP
		}
		hop = hop.Unmap()
		if !isTrusted(hop, trusted) {
			return hop
		}
	}
	return remoteIP
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
