package httpx

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	idleLimiterTTL = 10 * time.Minute
	sweepInterval  = time.Minute
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type limiterSet struct {
	mu        sync.Mutex
	visitors  map[string]*visitor
	limit     rate.Limit
	burst     int
	now       func() time.Time
	lastSweep time.Time
}

func newLimiterSet(perMinute, burst int, now func() time.Time) *limiterSet {
	return &limiterSet{
		visitors:  make(map[string]*visitor),
		limit:     rate.Limit(float64(perMinute) / time.Minute.Seconds()),
		burst:     burst,
		now:       now,
		lastSweep: now(),
	}
}

func (s *limiterSet) allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweep(now)
	v, found := s.visitors[key]
	if !found {
		v = &visitor{limiter: rate.NewLimiter(s.limit, s.burst)}
		s.visitors[key] = v
	}
	v.lastSeen = now
	return v.limiter.AllowN(now, 1)
}

func (s *limiterSet) sweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for key, v := range s.visitors {
		if now.Sub(v.lastSeen) >= idleLimiterTTL {
			delete(s.visitors, key)
		}
	}
}

func (s *limiterSet) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.visitors)
}

func RateLimit(perMinute int, burst int, trusted []netip.Prefix) func(http.Handler) http.Handler {
	set := newLimiterSet(perMinute, burst, time.Now)
	retryAfter := strconv.Itoa(int(math.Ceil(time.Minute.Seconds() / float64(perMinute))))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !set.allow(ClientIP(r, trusted).String()) {
				w.Header().Set("Retry-After", retryAfter)
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
