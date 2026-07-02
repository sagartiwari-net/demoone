package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	sessionActivateMu   sync.Mutex
	sessionActivatedKey string
	sessionActivatedAt  time.Time
	sessionActivateTTL  = 4 * time.Minute
)

type proxyBodyCacheKeyType struct{}

var proxyBodyCacheKey = proxyBodyCacheKeyType{}

func cacheRequestBodyForRetry(req *http.Request) {
	if req == nil || req.Body == nil {
		return
	}
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return
	}
	if !isSemrushAPIRequest(req) {
		return
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	req.ContentLength = int64(len(bodyBytes))
	*req = *req.WithContext(context.WithValue(req.Context(), proxyBodyCacheKey, bodyBytes))
}

func semrushDeviceFingerprint(cfg Config, cookieStr string) string {
	sso := ""
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "sso_token=") {
			sso = strings.TrimPrefix(part, "sso_token=")
			break
		}
	}
	base := cfg.UserAgent + "|" + sso
	if sso == "" {
		base = cfg.UserAgent + "|" + cookieStr
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:16])
}

func parseMultiloginRedirect(location string) string {
	if location == "" {
		return ""
	}
	u, err := url.Parse(location)
	if err != nil {
		return ""
	}
	path := u.Path
	if !strings.Contains(strings.ToLower(path), "multilogin") {
		return ""
	}
	redirectTo := u.Query().Get("redirect_to")
	if redirectTo == "" || !strings.HasPrefix(redirectTo, "/") || strings.HasPrefix(redirectTo, "//") {
		return ""
	}
	return redirectTo
}

func ensureSemrushSessionActivated(cfg Config, cookieStr string, force bool) {
	cookieStr = strings.TrimSpace(cookieStr)
	if cookieStr == "" {
		return
	}
	fp := semrushDeviceFingerprint(cfg, cookieStr)
	key := fp + "|" + cookieHash(cookieStr)

	sessionActivateMu.Lock()
	defer sessionActivateMu.Unlock()
	if !force && sessionActivatedKey == key && time.Since(sessionActivatedAt) < sessionActivateTTL {
		return
	}

	body := fmt.Sprintf(`{"user-agent-hash":"%s"}`, fp)
	req, err := http.NewRequest(http.MethodPost, "https://www.semrush.com/sso/user-sessions/activate", bytes.NewReader([]byte(body)))
	if err != nil {
		log.Printf("[SESSION] activate build request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Custom-User-Hash", fp)
	req.Header.Set("Cookie", cookieStr)
	req.Header.Set("User-Agent", cfg.UserAgent)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[SESSION] activate request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		sessionActivatedKey = key
		sessionActivatedAt = time.Now()
		log.Printf("[SESSION] ✅ activated upstream session (status=%d fp=%s...)", resp.StatusCode, fp[:8])
	} else {
		log.Printf("[SESSION] ⚠️ activate failed status=%d fp=%s...", resp.StatusCode, fp[:8])
	}
}

func cookieHash(cookieStr string) string {
	sum := sha256.Sum256([]byte(cookieStr))
	return hex.EncodeToString(sum[:8])
}

func applySemrushSessionHeaders(req *http.Request, cfg Config, cookieStr string) {
	fp := semrushDeviceFingerprint(cfg, cookieStr)
	if fp == "" {
		return
	}
	req.Header.Set("Custom-User-Hash", fp)
}

// Raw JS only — wrapped once by blockScript in main.go (no nested <script> tags).
func cookieValueFromString(cookieStr, name string) string {
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+"=") {
			return strings.TrimPrefix(part, name+"=")
		}
	}
	return ""
}

// Injects SSO-JWT for gRPC-web (auth-data-jwt header). Browser cookies for 127.0.0.1
// do not include Semrush session data, so widget APIs never authenticate without this.
func buildGrpcWebFix(ssoJWT string) string {
	if strings.TrimSpace(ssoJWT) == "" {
		return ""
	}
	return fmt.Sprintf(`
(function(){
  var JWT = %q;
  var _setHeader = XMLHttpRequest.prototype.setRequestHeader;
  XMLHttpRequest.prototype.setRequestHeader = function(name, value) {
    var n = String(name || "").toLowerCase();
    if (n === "auth-data-jwt") {
      var v = value == null ? "" : String(value);
      if (!v || v === "null" || v === "undefined") value = JWT;
    }
    return _setHeader.call(this, name, value);
  };
})();
`, ssoJWT)
}

func buildSemrushSessionBootstrapJS(deviceFP string) string {
	return fmt.Sprintf(`
(function(){
  var FP = %q;
  function activateSession() {
    return fetch("/sso/user-sessions/activate", {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type":"application/json","Custom-User-Hash": FP},
      body: JSON.stringify({"user-agent-hash": FP})
    }).catch(function(){});
  }
  activateSession();
  if (window.location.pathname.indexOf("/multilogin") !== -1) {
    activateSession().then(function(){
      var p = new URLSearchParams(window.location.search);
      var to = p.get("redirect_to");
      if (to && to.charAt(0) === "/") window.location.replace(to);
    });
  }
})();
`, deviceFP)
}

func isSemrushAPIRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	p := strings.ToLower(r.URL.Path)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	return strings.Contains(p, "/api/") || strings.Contains(p, "/widget/") || strings.Contains(p, "/rpc")
}

func retrySemrushUpstream(orig *http.Request, cfg Config, cookieStr string) (*http.Response, error) {
	if orig == nil || orig.URL == nil {
		return nil, fmt.Errorf("no request")
	}
	ensureSemrushSessionActivated(cfg, cookieStr, true)

	upstreamURL := "https://www.semrush.com" + orig.URL.Path
	if orig.URL.RawQuery != "" {
		upstreamURL += "?" + orig.URL.RawQuery
	}

	var body io.Reader
	if orig.Method != http.MethodGet && orig.Method != http.MethodHead {
		if cached, ok := orig.Context().Value(proxyBodyCacheKey).([]byte); ok && len(cached) > 0 {
			body = bytes.NewReader(cached)
		}
	}

	req, err := http.NewRequestWithContext(orig.Context(), orig.Method, upstreamURL, body)
	if err != nil {
		return nil, err
	}
	req.Host = "www.semrush.com"

	for k, vals := range orig.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "cookie" || strings.HasPrefix(lk, "x-proxy-") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Cookie", cookieStr)
	req.Header.Set("User-Agent", cfg.UserAgent)
	applySemrushSessionHeaders(req, cfg, cookieStr)

	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := strings.ToLower(resp.Header.Get("Location"))
		if strings.Contains(loc, "multilogin") {
			resp.Body.Close()
			return nil, fmt.Errorf("multilogin persists after activate")
		}
	}
	return resp, nil
}
