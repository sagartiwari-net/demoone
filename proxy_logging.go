package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type proxyLogKeyType struct{}

var proxyLogKey = proxyLogKeyType{}

func debugEnabled(cfg Config) bool {
	return cfg.LocalTestMode || cfg.DebugLogging
}

type requestLogger struct {
	start      time.Time
	reqHost    string
	method     string
	path       string
	query      string
	origin     string
	clientIP   string
	upstream   string
	status     int
	upstreamMS int64
	category   string
	errMsg     string
	cookieInfo string
	bodyHint   string
}

type proxyDiagCounters struct {
	sync.Mutex
	total, api2xx, api4xx, api5xx, multilogin, errors int
}

var proxyDiag proxyDiagCounters

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func classifyPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "/multilogin"):
		return "MULTILOGIN"
	case strings.HasPrefix(lower, "/dpa/"):
		return "API"
	case strings.Contains(lower, "/rpc"):
		return "API"
	case strings.HasPrefix(lower, "/api/"):
		return "API"
	case strings.Contains(lower, "/seo/api/"):
		return "API"
	case strings.Contains(lower, "/analytics/"):
		return "API"
	case strings.HasPrefix(lower, "/static-proxy/"):
		return "STATIC"
	case strings.HasPrefix(lower, "/secure-proxy/"):
		return "SECURE"
	case strings.HasPrefix(lower, "/cdn-proxy/"):
		return "CDN"
	case strings.HasPrefix(lower, "/ai-proxy/"):
		return "AI"
	case strings.HasSuffix(lower, ".js"):
		return "JS"
	case strings.HasSuffix(lower, ".css"):
		return "CSS"
	default:
		return "OTHER"
	}
}

var semrushAuthCookies = []string{"sso_token", "SSO-JWT", "PHPSESSID", "site_csrftoken", "GCLB"}

func summarizeSemrushCookies(cookieHeader string, upstreamCookie string) string {
	flags := make([]string, 0, 8)
	check := cookieHeader
	if upstreamCookie != "" {
		check = upstreamCookie
	}
	for _, name := range semrushAuthCookies {
		if strings.Contains(check, name+"=") {
			flags = append(flags, name+"=yes")
		} else {
			flags = append(flags, name+"=no")
		}
	}
	src := "browser"
	if upstreamCookie != "" && cookieHeader == "" {
		src = "cookie.txt"
	} else if upstreamCookie != "" {
		src = "merged"
	}
	return src + "_" + strings.Join(flags, " ")
}

func truncateLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func bodyDiagnostic(status int, peek []byte) string {
	if len(peek) == 0 {
		return ""
	}
	s := string(peek)
	lower := strings.ToLower(s)
	if strings.Contains(lower, "too many active sessions") || strings.Contains(lower, "multilogin") {
		return "MULTILOGIN_PAGE"
	}
	if status == 401 {
		return "UNAUTHORIZED"
	}
	if status == 403 {
		return truncateLog(s, 100)
	}
	if status >= 400 {
		return truncateLog(s, 80)
	}
	return ""
}

func (rl *requestLogger) finish(cfg Config) {
	if !debugEnabled(cfg) {
		return
	}
	totalMS := time.Since(rl.start).Milliseconds()
	cat := rl.category
	if cat == "" {
		cat = classifyPath(rl.path)
	}

	proxyDiag.Lock()
	proxyDiag.total++
	switch cat {
	case "API", "AI", "SECURE":
		switch {
		case rl.status >= 200 && rl.status < 300:
			proxyDiag.api2xx++
		case rl.status >= 400 && rl.status < 500:
			proxyDiag.api4xx++
		default:
			proxyDiag.api5xx++
		}
	case "MULTILOGIN":
		proxyDiag.multilogin++
	}
	if rl.errMsg != "" {
		proxyDiag.errors++
	}
	proxyDiag.Unlock()

	if (cat == "JS" || cat == "STATIC" || cat == "CDN") && rl.errMsg == "" && rl.status >= 200 && rl.status < 400 && totalMS < 400 {
		return
	}

	q := rl.query
	if q != "" {
		q = "?" + q
	}
	msg := fmt.Sprintf("[PROXY:%s] %s %s%s | host=%q status=%d total=%dms ip=%s",
		cat, rl.method, rl.path, q, rl.reqHost, rl.status, totalMS, rl.clientIP)
	if rl.upstream != "" {
		msg += " | up=" + truncateLog(rl.upstream, 60)
	}
	if rl.cookieInfo != "" {
		msg += " | " + rl.cookieInfo
	}
	if rl.bodyHint != "" {
		msg += " | hint=" + rl.bodyHint
	}
	if rl.errMsg != "" {
		msg += " | ERR=" + truncateLog(rl.errMsg, 120)
	}
	if rl.origin != "" {
		msg += " | origin=" + rl.origin
	}
	log.Println(msg)

	if cat == "MULTILOGIN" || strings.Contains(rl.bodyHint, "MULTILOGIN") {
		log.Printf("[MULTILOGIN] ⚠️ Semrush session conflict — account open elsewhere OR browser cookies mixed with cookie.txt. Local mode uses cookie.txt only now.")
	}
	if cat == "API" && rl.status == 401 {
		log.Printf("[PROXY:WARN] 401 on %s — cookie.txt expired or missing sso_token/SSO-JWT", rl.path)
	}
	if cat == "API" && rl.status >= 400 {
		log.Printf("[PROXY:WARN] API error %d on %s %s", rl.status, rl.method, rl.path)
	}
}

func startStatsReporter(cfg Config) {
	if !debugEnabled(cfg) {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			proxyDiag.Lock()
			s := proxyDiag
			proxyDiag.total = 0
			proxyDiag.api2xx = 0
			proxyDiag.api4xx = 0
			proxyDiag.api5xx = 0
			proxyDiag.multilogin = 0
			proxyDiag.errors = 0
			proxyDiag.Unlock()
			if s.total == 0 {
				continue
			}
			log.Printf("[PROXY:STATS] 30s | total=%d api_2xx=%d api_4xx=%d api_5xx=%d multilogin=%d errors=%d",
				s.total, s.api2xx, s.api4xx, s.api5xx, s.multilogin, s.errors)
			if s.multilogin > 0 {
				log.Printf("[PROXY:STATS] ⚠️ multilogin hits=%d — close real semrush.com tabs or refresh cookie.txt", s.multilogin)
			}
			if s.api4xx > 0 && s.api2xx == 0 {
				log.Printf("[PROXY:STATS] ⚠️ All API calls failed — re-export fresh cookie.txt from logged-in semrush.com")
			}
		}
	}()
}

func logStartupDiagnostics(cfg Config) {
	if !debugEnabled(cfg) {
		return
	}
	log.Printf("[PROXY:STARTUP] debug=ON local_test=%v", cfg.LocalTestMode)
	log.Printf("[PROXY:STARTUP] open http://%s | cookie_file=%s", cfg.PublicHost, cookieFilePath(cfg))
	if _, err := os.Stat(cookieFilePath(cfg)); err != nil {
		log.Printf("[PROXY:STARTUP] ❌ cookie file missing: %s", cookieFilePath(cfg))
	} else {
		ck := loadCookies(cfg)
		log.Printf("[PROXY:STARTUP] %s", summarizeSemrushCookies("", ck))
	}
	log.Printf("[PROXY:STARTUP] Log tags: [PROXY:API] [PROXY:WARN] [MULTILOGIN] [PROXY:STATS]")
}

func getRequestLogger(ctx context.Context) *requestLogger {
	if rl, ok := ctx.Value(proxyLogKey).(*requestLogger); ok {
		return rl
	}
	return nil
}

func mergeCookiesForUpstream(cfg Config, clientCookieStr, premiumCookieStr string) string {
	if cfg.LocalTestMode && premiumCookieStr != "" {
		return premiumCookieStr
	}
	return mergeCookies(clientCookieStr, premiumCookieStr)
}

const multiloginAutoScript = `<script>
(function(){
  function clickContinue(){
    var buttons = document.querySelectorAll('button');
    for (var i = 0; i < buttons.length; i++) {
      var t = (buttons[i].textContent || '').trim().toLowerCase();
      if (t === 'continue') { buttons[i].click(); return true; }
    }
    return false;
  }
  if (window.location.pathname.indexOf('/multilogin') !== -1) {
    setTimeout(clickContinue, 600);
    setTimeout(clickContinue, 1800);
  }
})();
</script>`
