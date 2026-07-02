package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func getConfigFile() string {
	if p := strings.TrimSpace(os.Getenv("CONFIG_FILE")); p != "" {
		return p
	}
	return "config.json"
}

func cookieFilePath(cfg Config) string {
	if strings.TrimSpace(cfg.CookieFile) != "" {
		return cfg.CookieFile
	}
	return "cookie.txt"
}

// Config structures
type Config struct {
	Port         string `json:"port"`
	TargetURL    string `json:"target_url"`
	PublicHost   string `json:"public_host"`
	PublicScheme string `json:"public_scheme"`
	UserAgent    string `json:"user_agent"`
	WebsiteID    int    `json:"website_id"`
	CookieFile   string `json:"cookie_file"`
	LocalTestMode bool  `json:"local_test_mode"`
	BindLocalhost bool  `json:"bind_localhost"`
	DebugLogging  bool  `json:"debug_logging"`
	// MySQL configs
	MySQLHost     string `json:"mysql_host"`
	MySQLPort     string `json:"mysql_port"`
	MySQLUser     string `json:"mysql_user"`
	MySQLPassword string `json:"mysql_password"`
	MySQLDB       string `json:"mysql_db"`
}

var (
	currentConfig Config
	configModTime time.Time
	
	// DB handles
	db               *sql.DB

	// Regexp for stripping Subresource Integrity (SRI)
	integrityRegex      = regexp.MustCompile(`(?i)\s*integrity=(?:"[^"]*"|'[^']*')`)
	webpackAttrRegex    = regexp.MustCompile(`(?i)(?:"integrity"|'integrity')\s*:`)
	webpackSetAttrRegex = regexp.MustCompile(`(?i)setAttribute\(\s*[\'"]integrity[\'"]`)

	// Ultra-fast in-memory asset cache
	cacheMutex sync.RWMutex
	assetCache = make(map[string]CachedResponse)
)

type CachedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// isCacheablePath checks if the URL path can be cached
func isCacheablePath(path string) bool {
	if !strings.HasPrefix(path, "/static-proxy/") && !strings.HasPrefix(path, "/secure-proxy/") && !strings.HasPrefix(path, "/cdn-proxy/") && !strings.HasPrefix(path, "/ai-proxy/") {
		return false
	}
	// Cache static extensions
	exts := []string{".js", ".css", ".woff", ".woff2", ".ttf", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico"}
	lowerPath := strings.ToLower(path)
	for _, ext := range exts {
		if strings.HasSuffix(lowerPath, ext) || strings.Contains(lowerPath, ext+"?") {
			return true
		}
	}
	return false
}

// loadConfig loads the configuration from config.json with hot-reloading
func loadConfig() Config {
	configFile := getConfigFile()
	info, err := os.Stat(configFile)
	if err != nil {
		return currentConfig
	}
	if !info.ModTime().After(configModTime) {
		return currentConfig
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		log.Printf("[CONFIG] Error reading config file: %v", err)
		return currentConfig
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[CONFIG] Error parsing config JSON: %v", err)
		return currentConfig
	}

	if cfg.Port == "" {
		cfg.Port = "7850"
	}
	if cfg.TargetURL == "" {
		cfg.TargetURL = "https://www.semrush.com"
	}
	if cfg.PublicHost == "" {
		cfg.PublicHost = "localhost:" + cfg.Port
	}
	if cfg.PublicScheme == "" {
		cfg.PublicScheme = "http"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
	}

	// Default Semrush WebsiteID if missing
	if cfg.WebsiteID == 0 {
		cfg.WebsiteID = 3
	}
	if cfg.CookieFile == "" {
		cfg.CookieFile = "cookie.txt"
	}

	// Default MySQL connections only when not in local cookie-file mode
	if !cfg.LocalTestMode && cfg.MySQLHost != "" {
		if cfg.MySQLPort == "" {
			cfg.MySQLPort = "3306"
		}
		if cfg.MySQLUser == "" {
			cfg.MySQLUser = "root"
		}
		if cfg.MySQLDB == "" {
			cfg.MySQLDB = "toolsmandirefct"
		}
	}

	currentConfig = cfg
	configModTime = info.ModTime()
	log.Printf("[CONFIG] Config loaded successfully (Port: %s, Host: %s, WebsiteID: %d) ✅", cfg.Port, cfg.PublicHost, cfg.WebsiteID)
	return cfg
}

// mergeCookies merges incoming client cookies with the premium cookies from cookie.txt.
// Premium cookies override incoming client cookies if there are conflicts.
func mergeCookies(clientCookieStr, premiumCookieStr string) string {
	if premiumCookieStr == "" {
		return clientCookieStr
	}
	if clientCookieStr == "" {
		return premiumCookieStr
	}

	// Parse client cookies
	cookies := make(map[string]string)
	parts := strings.Split(clientCookieStr, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		subparts := strings.SplitN(part, "=", 2)
		if len(subparts) == 2 {
			cookies[subparts[0]] = subparts[1]
		}
	}

	// Overwrite/Merge with premium cookies
	premiumParts := strings.Split(premiumCookieStr, ";")
	for _, part := range premiumParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		subparts := strings.SplitN(part, "=", 2)
		if len(subparts) == 2 {
			cookies[subparts[0]] = subparts[1]
		}
	}

	// Build merged cookie string
	var merged []string
	for k, v := range cookies {
		merged = append(merged, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(merged, "; ")
}

func resolveWebsiteID(publicHost string) int {
	if wid := lookupWebsiteIDByHost(publicHost); wid > 0 {
		log.Printf("[DB] Resolved website_id = %d for domain '%s' ✅", wid, normalizeHost(publicHost))
		return wid
	}
	cfg := loadConfig()
	if cfg.WebsiteID > 0 {
		return cfg.WebsiteID
	}
	log.Printf("[DB] ⚠️ Domain '%s' not registered — using website_id = 1", normalizeHost(publicHost))
	return 1
}

type Account struct {
	ID        int
	Name      string
	Cookie    string
	UserAgent string
}

func selectActiveAccount(websiteID int) (Account, error) {
	var acc Account
	query := "SELECT id, name, cookie, user_agent FROM ahrefs_accounts WHERE website_id = ? AND status = 'active' ORDER BY last_used_at ASC LIMIT 1"
	err := db.QueryRow(query, websiteID).Scan(&acc.ID, &acc.Name, &acc.Cookie, &acc.UserAgent)
	if err != nil {
		return acc, err
	}
	
	// Update last_used_at to rotate account usage
	_, _ = db.Exec("UPDATE ahrefs_accounts SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", acc.ID)
	return acc, nil
}

func realClientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Original-Forwarded-For"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func generateSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sanitizeCookieHeader removes invalid HTTP/2 header characters from a cookie string.
func sanitizeCookieHeader(s string) string {
	replacer := strings.NewReplacer("\r", "", "\n", "", "\x00", "", "\t", " ")
	return strings.TrimSpace(replacer.Replace(s))
}

// parseCookieFromDB converts the stored cookie to HTTP Cookie header format.
func parseCookieFromDB(raw string) string {
	raw = sanitizeCookieHeader(raw)
	if raw == "" {
		return ""
	}

	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "[") {
		return raw
	}

	var cookies []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(trimmed), &cookies); err != nil {
		log.Printf("[COOKIE] ⚠️ Could not parse JSON cookie array: %v — using raw value", err)
		return raw
	}

	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name != "" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	result := strings.Join(parts, "; ")
	return result
}

func renderAccessDeniedPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Access Denied — ToolsMandi</title>
    <link href="https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;600;800&display=swap" rel="stylesheet">
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: 'Outfit', sans-serif;
            background: radial-gradient(circle at center, #111b2d 0%, #080c14 100%);
            color: #ffffff;
            height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            overflow: hidden;
        }
        .container {
            text-align: center;
            padding: 40px;
            background: rgba(255, 255, 255, 0.03);
            border: 1px solid rgba(255, 255, 255, 0.05);
            border-radius: 24px;
            backdrop-filter: blur(16px);
            box-shadow: 0 20px 50px rgba(0, 0, 0, 0.5);
            max-width: 500px;
            width: 90%;
            transform: translateY(0);
            transition: all 0.3s ease;
        }
        .container:hover {
            transform: translateY(-5px);
            border-color: rgba(255, 99, 102, 0.2);
            box-shadow: 0 30px 60px rgba(255, 99, 102, 0.1);
        }
        .icon {
            font-size: 64px;
            margin-bottom: 20px;
            display: inline-block;
            animation: pulse 2s infinite;
        }
        h1 {
            font-size: 32px;
            font-weight: 800;
            margin-bottom: 12px;
            background: linear-gradient(135deg, #ff6366 0%, #ff8e53 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        p {
            font-size: 16px;
            color: #a0aec0;
            line-height: 1.6;
            margin-bottom: 30px;
        }
        .btn {
            display: inline-block;
            padding: 14px 32px;
            background: linear-gradient(135deg, #ff6366 0%, #ff8e53 100%);
            color: #ffffff;
            font-weight: 600;
            text-decoration: none;
            border-radius: 12px;
            transition: all 0.2s ease;
            box-shadow: 0 8px 20px rgba(255, 99, 102, 0.3);
        }
        .btn:hover {
            transform: scale(1.05);
            box-shadow: 0 12px 25px rgba(255, 99, 102, 0.5);
        }
        @keyframes pulse {
            0% { transform: scale(1); }
            50% { transform: scale(1.1); }
            100% { transform: scale(1); }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="icon">🔒</div>
        <h1>Access Denied</h1>
        <p>You do not have permission to access this tool directly. Please use the <strong>aMemberPro Member Area</strong> to log in.</p>
        <a href="https://toolsmandi.com/member" class="btn">Go to Member Area</a>
    </div>
</body>
</html>`)
}

// loadCookies reads the cookie string from cookie.txt
func loadCookies(cfg Config) string {
	data, err := os.ReadFile(cookieFilePath(cfg))
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}

	// 1. If it's a JSON array, parse dynamically
	if strings.HasPrefix(trimmed, "[") {
		var cookieList []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(trimmed), &cookieList); err == nil {
			var cookieParts []string
			for _, c := range cookieList {
				if c.Name != "" && c.Value != "" {
					cookieParts = append(cookieParts, fmt.Sprintf("%s=%s", c.Name, c.Value))
				}
			}
			return strings.Join(cookieParts, "; ")
		}
	}

	// 2. Raw Cookie text string fallback
	lines := strings.Split(trimmed, "\n")
	var cookieParts []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		cookieParts = append(cookieParts, line)
	}
	return strings.Join(cookieParts, "; ")
}

// detectLimitOrLogout scans Semrush response body for free account limits, upgrade CTAs, or logouts.
func detectLimitOrLogout(body string) bool {
	// 1. Upgrade button (using the specific ID)
	if strings.Contains(body, `id="srf-header-upgrade-button"`) || 
		strings.Contains(body, `id='srf-header-upgrade-button'`) || 
		strings.Contains(body, `srf-header-upgrade-button`) {
		return true
	}
	// 2. Used 10 free requests limit
	if strings.Contains(body, `You’ve used 10 free requests`) || 
		strings.Contains(body, `You've used 10 free requests`) {
		return true
	}
	// 3. Register to get 10 free requests
	if strings.Contains(body, `Register to get 10 free requests`) {
		return true
	}
	// 4. Log In button (indicates account is logged out of upstream)
	if strings.Contains(body, `srf-login-btn`) || strings.Contains(body, `auth-popup__btn-login`) {
		return true
	}
	// 5. Already registered? (Only in SSO registration header context to prevent generic page matches)
	if strings.Contains(body, `Already registered?`) && 
		(strings.Contains(body, `sso-header`) || strings.Contains(body, `Register to get`)) {
		return true
	}
	return false
}

// renderSwappingPage returns a gorgeous premium loading screen with custom redirect action
func renderSwappingPage(redirectTo string) string {
	script := "window.location.reload();"
	if redirectTo != "" {
		script = fmt.Sprintf("window.location.href = %q;", redirectTo)
	}

	return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Rotating Premium Session — ToolsMandi</title>
    <link href="https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;600;800&display=swap" rel="stylesheet">
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: 'Outfit', sans-serif;
            background: radial-gradient(circle at center, #0f172a 0%, #020617 100%);
            color: #ffffff;
            height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            overflow: hidden;
        }
        .container {
            text-align: center;
            padding: 40px 60px;
            background: rgba(255, 255, 255, 0.03);
            border: 1px solid rgba(255, 255, 255, 0.05);
            border-radius: 24px;
            backdrop-filter: blur(20px);
            box-shadow: 0 20px 50px rgba(0, 0, 0, 0.6), 0 0 40px rgba(99, 102, 241, 0.1);
            max-width: 500px;
            width: 90%;
            animation: fadeIn 0.6s ease-out;
        }
        .loader-wrapper {
            position: relative;
            width: 80px;
            height: 80px;
            margin: 0 auto 30px auto;
        }
        .loader {
            width: 100%;
            height: 100%;
            border: 4px solid rgba(99, 102, 241, 0.1);
            border-top-color: #6366f1;
            border-radius: 50%;
            animation: spin 1s cubic-bezier(0.5, 0, 0.5, 1) infinite;
        }
        .loader-inner {
            position: absolute;
            top: 10px;
            left: 10px;
            right: 10px;
            bottom: 10px;
            border: 4px solid rgba(236, 72, 153, 0.1);
            border-top-color: #ec4899;
            border-radius: 50%;
            animation: spin-reverse 1.5s cubic-bezier(0.5, 0, 0.5, 1) infinite;
        }
        .glow {
            position: absolute;
            top: 50%;
            left: 50%;
            transform: translate(-50%, -50%);
            width: 120px;
            height: 120px;
            background: radial-gradient(circle, rgba(99, 102, 241, 0.15) 0%, rgba(99, 102, 241, 0) 70%);
            animation: pulse 2s ease-in-out infinite;
        }
        h1 {
            font-size: 26px;
            font-weight: 800;
            margin-bottom: 12px;
            background: linear-gradient(135deg, #6366f1 0%, #ec4899 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            letter-spacing: -0.5px;
        }
        p {
            font-size: 15px;
            color: #94a3b8;
            line-height: 1.6;
            font-weight: 400;
        }
        .dots {
            display: inline-block;
            margin-left: 2px;
            animation: blink 1.4s infinite both;
        }
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
        @keyframes spin-reverse {
            0% { transform: rotate(360deg); }
            100% { transform: rotate(0deg); }
        }
        @keyframes pulse {
            0%, 100% { transform: translate(-50%, -50%) scale(0.9); opacity: 0.5; }
            50% { transform: translate(-50%, -50%) scale(1.1); opacity: 1; }
        }
        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(10px); }
            to { opacity: 1; transform: translateY(0); }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="loader-wrapper">
            <div class="glow"></div>
            <div class="loader"></div>
            <div class="loader-inner"></div>
        </div>
        <h1>Rotating Session</h1>
        <p>Switching to a fresh premium account for uninterrupted access. Please wait<span class="dots">...</span></p>
    </div>
    <script>
        setTimeout(function() {
            ` + script + `
        }, 1800);
    </script>
</body>
</html>`
}

func buildLocalHostFix(cfg Config) string {
	if !cfg.LocalTestMode {
		return ""
	}
	fakeHostname := "www.semrush.com"
	if u, err := url.Parse(cfg.TargetURL); err == nil && u.Hostname() != "" {
		fakeHostname = u.Hostname()
	}
	proxyHostname := cfg.PublicHost
	if host, _, err := net.SplitHostPort(cfg.PublicHost); err == nil {
		proxyHostname = host
	}
	return fmt.Sprintf(`
try {
    var REAL_PROXY_ORIGIN = window.location.origin;
    var REAL_PROXY_HOST = window.location.host;
    var REAL_PROXY_PROTOCOL = window.location.protocol;
    var FAKE_NAME = %q;
    var PROXY_NAME = %q;
    function isProxyHost(h) {
        return h === '127.0.0.1' || h === 'localhost' || h === PROXY_NAME;
    }
    // Only spoof hostname — NOT origin/host/protocol.
    // app-bootstrap builds gRPC base from location.origin + "/seo/api";
    // spoofing origin sent Widget calls to real semrush.com (invisible in proxy Network tab).
    var _hostDesc = Object.getOwnPropertyDescriptor(Location.prototype, 'hostname');
    if (_hostDesc && _hostDesc.get) {
        var _realHostGet = _hostDesc.get;
        Object.defineProperty(Location.prototype, 'hostname', {
            get: function() {
                var h = _realHostGet.call(this);
                if (isProxyHost(h)) return FAKE_NAME;
                return h;
            },
            configurable: true
        });
    }
} catch(e) {}
`, fakeHostname, proxyHostname)
}

func main() {
	cfg := loadConfig()

	if cfg.LocalTestMode || strings.TrimSpace(cfg.MySQLHost) == "" {
		log.Printf("[LOCAL] local_test_mode — using %s (no MySQL auth)", cookieFilePath(cfg))
		db = nil
	} else {
		// Initialize MySQL DB connection if available
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=Asia%%2FKolkata",
			cfg.MySQLUser, cfg.MySQLPassword, cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLDB)
		var err error
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			log.Printf("[DB] ⚠️ MySQL Connection failed to initialize: %v", err)
		} else {
			db.SetMaxOpenConns(25)
			db.SetMaxIdleConns(5)
			db.SetConnMaxLifetime(5 * time.Minute)
			if err := db.Ping(); err != nil {
				log.Printf("[DB] ⚠️ Could not connect to MySQL: %v. Database mode is offline.", err)
			} else {
				log.Printf("[DB] Connected to MySQL successfully! Database: %s ✅", cfg.MySQLDB)
				_ = resolveWebsiteID(cfg.PublicHost)
			}
		}
	}

	targetUrl, err := url.Parse(cfg.TargetURL)
	if err != nil {
		log.Fatalf("[FATAL] Invalid target URL: %v", err)
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetUrl)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if rl := getRequestLogger(r.Context()); rl != nil {
			rl.errMsg = err.Error()
			rl.status = http.StatusBadGateway
		}
		log.Printf("[PROXY:ERR] %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Proxy Director (already handles dynamic DB loading we added)
	proxy.Director = func(req *http.Request) {
		cfg = loadConfig()
		targetHost := "www.semrush.com"
		if strings.HasPrefix(req.URL.Path, "/secure-proxy/") || req.URL.Path == "/secure-proxy" {
			targetHost = "secure.semrush.com"
			req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/secure-proxy"), "/")
		} else if strings.HasPrefix(req.URL.Path, "/static-proxy/") || req.URL.Path == "/static-proxy" {
			targetHost = "static.semrush.com"
			req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/static-proxy"), "/")
		} else if strings.HasPrefix(req.URL.Path, "/cdn-proxy/") || req.URL.Path == "/cdn-proxy" {
			targetHost = "cdn.semrush.com"
			req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/cdn-proxy"), "/")
		} else if strings.HasPrefix(req.URL.Path, "/ai-proxy/") || req.URL.Path == "/ai-proxy" {
			targetHost = "ai-visibility-index.semrush.com"
			req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/ai-proxy"), "/")
		}

		// Dynamic public host & scheme detection
		publicHost := req.Host
		if publicHost == "" {
			publicHost = cfg.PublicHost
		}
		publicScheme := cfg.PublicScheme
		if req.TLS != nil {
			publicScheme = "https"
		} else if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
			publicScheme = proto
		} else if publicScheme == "" {
			publicScheme = "http"
		}

		req.Header.Set("X-Proxy-Host", publicHost)
		req.Header.Set("X-Proxy-Scheme", publicScheme)

		req.Header.Set("Host", targetHost)
		req.Host = targetHost
		req.URL.Host = targetHost
		req.URL.Scheme = "https"

		// Set Browser Spoof Headers & Database Dynamic Cookie Fetching
		var dbCookie, dbUserAgent string
		var assignedAccountID sql.NullInt64
		var accID int

		if db != nil {
			websiteID := tenantWebsiteID(req.Context())
			if websiteID <= 0 {
				if sessionCookie, errC := req.Cookie("sem_session"); errC == nil && sessionCookie != nil && sessionCookie.Value != "" {
					_ = db.QueryRow("SELECT website_id FROM ahrefs_sessions WHERE session_token = ?", sessionCookie.Value).Scan(&websiteID)
				}
			}
			if websiteID <= 0 {
				websiteID = cfg.WebsiteID
			}
			if websiteID <= 0 {
				websiteID = 1
			}

			sessionCookie, errC := req.Cookie("sem_session")
			if errC == nil && sessionCookie != nil && sessionCookie.Value != "" {
				_ = db.QueryRow("SELECT assigned_account_id FROM ahrefs_sessions WHERE session_token = ? AND website_id = ?", sessionCookie.Value, websiteID).Scan(&assignedAccountID)
			}

			if assignedAccountID.Valid {
				errAcc := db.QueryRow("SELECT id, cookie, user_agent FROM ahrefs_accounts WHERE id = ? AND website_id = ? AND status = 'active'", assignedAccountID.Int64, websiteID).Scan(&accID, &dbCookie, &dbUserAgent)
				if errAcc != nil {
					assignedAccountID.Valid = false
				}
			}

			if !assignedAccountID.Valid {
				acc, errAcc := selectActiveAccount(websiteID)
				if errAcc == nil {
					accID = acc.ID
					dbCookie = acc.Cookie
					dbUserAgent = acc.UserAgent
					sessionCookie, errC := req.Cookie("sem_session")
					if errC == nil && sessionCookie != nil && sessionCookie.Value != "" {
						_, _ = db.Exec("UPDATE ahrefs_sessions SET assigned_account_id = ? WHERE session_token = ? AND website_id = ?", accID, sessionCookie.Value, websiteID)
					}
				}
			}
		}

		if dbCookie == "" {
			dbCookie = loadCookies(cfg)
		}
		
		dbUserAgent = strings.TrimSpace(dbUserAgent)
		clientUA := req.Header.Get("User-Agent")
		if dbUserAgent == "" {
			if clientUA != "" {
				dbUserAgent = clientUA
			} else {
				dbUserAgent = cfg.UserAgent
			}
		}

		req.Header.Set("User-Agent", dbUserAgent)
		if dbUserAgent != clientUA {
			req.Header.Del("Sec-Ch-Ua")
			req.Header.Del("Sec-Ch-Ua-Mobile")
			req.Header.Del("Sec-Ch-Ua-Platform")
		}

		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Real-IP")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")

		clientCookies := req.Header.Get("Cookie")
		parsedDbCookie := parseCookieFromDB(dbCookie)
		mergedCookies := mergeCookiesForUpstream(cfg, clientCookies, parsedDbCookie)
		if mergedCookies != "" {
			ensureSemrushSessionActivated(cfg, mergedCookies, false)
			if strings.Contains(strings.ToLower(req.URL.Path), "/seo/api/") || strings.Contains(strings.ToLower(req.URL.Path), "/widget/") {
				ensureSemrushSessionActivated(cfg, mergedCookies, true)
			}
			applySemrushSessionHeaders(req, cfg, mergedCookies)
		}
		if rl := getRequestLogger(req.Context()); rl != nil && debugEnabled(cfg) {
			rl.upstream = targetHost
			rl.cookieInfo = summarizeSemrushCookies(clientCookies, mergedCookies)
		}
		if mergedCookies != "" {
			req.Header.Set("Cookie", mergedCookies)
		}

		if strings.Contains(strings.ToLower(req.URL.Path), "/seo/api/") {
			if jwt := cookieValueFromString(mergedCookies, "SSO-JWT"); jwt != "" {
				if v := strings.TrimSpace(req.Header.Get("auth-data-jwt")); v == "" || v == "null" {
					req.Header.Set("auth-data-jwt", jwt)
				}
			}
		}

		cacheRequestBodyForRetry(req)

		if ref := req.Header.Get("Referer"); ref != "" {
			proxyOriginHTTP := "http://" + publicHost
			proxyOriginHTTPS := "https://" + publicHost
			replacedRef := strings.ReplaceAll(ref, proxyOriginHTTP, "https://"+targetHost)
			replacedRef = strings.ReplaceAll(replacedRef, proxyOriginHTTPS, "https://"+targetHost)
			replacedRef = strings.ReplaceAll(replacedRef, cfg.PublicHost, targetHost)
			replacedRef = strings.ReplaceAll(replacedRef, "http://", "https://")
			req.Header.Set("Referer", replacedRef)
		}
		if origin := req.Header.Get("Origin"); origin != "" {
			proxyOriginHTTP := "http://" + publicHost
			proxyOriginHTTPS := "https://" + publicHost
			replacedOrigin := strings.ReplaceAll(origin, proxyOriginHTTP, "https://"+targetHost)
			replacedOrigin = strings.ReplaceAll(replacedOrigin, proxyOriginHTTPS, "https://"+targetHost)
			replacedOrigin = strings.ReplaceAll(replacedOrigin, cfg.PublicHost, targetHost)
			replacedOrigin = strings.ReplaceAll(replacedOrigin, "http://", "https://")
			req.Header.Set("Origin", replacedOrigin)
		}

		rawQuery := req.URL.RawQuery
		if rawQuery != "" {
			encodedProxy := url.QueryEscape(cfg.PublicScheme + "://" + cfg.PublicHost)
			encodedTarget := url.QueryEscape("https://" + targetHost)
			rawQuery = strings.ReplaceAll(rawQuery, encodedProxy, encodedTarget)
			encodedProxyNoScheme := url.QueryEscape(cfg.PublicHost)
			encodedTargetNoScheme := url.QueryEscape(targetHost)
			rawQuery = strings.ReplaceAll(rawQuery, encodedProxyNoScheme, encodedTargetNoScheme)
			rawQuery = strings.ReplaceAll(rawQuery, cfg.PublicHost, targetHost)
			req.URL.RawQuery = rawQuery
		}

		if req.Method == "POST" {
			req.Header.Set("X-Kl-Ajax-Request", "Ajax_Request")
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		cfg = loadConfig()

		// Retrieve dynamic proxy Host and Scheme from request headers
		proxyHost := ""
		proxyScheme := ""
		if resp.Request != nil {
			proxyHost = resp.Request.Header.Get("X-Proxy-Host")
			proxyScheme = resp.Request.Header.Get("X-Proxy-Scheme")
		}
		if proxyHost == "" {
			proxyHost = cfg.PublicHost
		}
		if proxyScheme == "" {
			proxyScheme = cfg.PublicScheme
		}

		if loc := resp.Header.Get("Location"); loc != "" {
			lowerLoc := strings.ToLower(loc)
			if strings.Contains(lowerLoc, "multilogin") {
				fromPath := ""
				if resp.Request != nil {
					fromPath = resp.Request.URL.Path
				}
				log.Printf("[MULTILOGIN] ⚠️ upstream redirect | from=%s → %s", fromPath, loc)
				if rl := getRequestLogger(resp.Request.Context()); rl != nil {
					rl.bodyHint = "MULTILOGIN_REDIRECT:" + truncateLog(loc, 80)
					rl.category = "MULTILOGIN"
				}
				cookieHdr := ""
				if resp.Request != nil {
					cookieHdr = resp.Request.Header.Get("Cookie")
				}
				if cookieHdr == "" {
					cookieHdr = loadCookies(cfg)
				}
				ensureSemrushSessionActivated(cfg, cookieHdr, true)
				if isSemrushAPIRequest(resp.Request) {
					if newResp, err := retrySemrushUpstream(resp.Request, cfg, cookieHdr); err == nil && newResp != nil {
						if resp.Body != nil {
							resp.Body.Close()
						}
						resp.StatusCode = newResp.StatusCode
						resp.Header = newResp.Header.Clone()
						resp.Body = newResp.Body
						resp.ContentLength = newResp.ContentLength
						resp.Header.Del("Location")
						loc = ""
						log.Printf("[MULTILOGIN] ✅ API retry %s %s → %d", resp.Request.Method, resp.Request.URL.Path, newResp.StatusCode)
						if rl := getRequestLogger(resp.Request.Context()); rl != nil {
							rl.bodyHint = fmt.Sprintf("MULTILOGIN_API_RETRY:%d", newResp.StatusCode)
						}
					} else if err != nil {
						log.Printf("[MULTILOGIN] ⚠️ API retry failed %s %s: %v", resp.Request.Method, resp.Request.URL.Path, err)
					}
				} else if redirectTo := parseMultiloginRedirect(loc); redirectTo != "" {
					replacedLoc := proxyScheme + "://" + proxyHost + redirectTo
					resp.Header.Set("Location", replacedLoc)
					log.Printf("[MULTILOGIN] ✅ auto-activated → sending browser to %s", redirectTo)
					if rl := getRequestLogger(resp.Request.Context()); rl != nil {
						rl.bodyHint = "MULTILOGIN_AUTO:" + redirectTo
					}
				}
			}
			// If it redirects to login or SSO pages, swap the account!
			if (strings.Contains(lowerLoc, "/login") || strings.Contains(lowerLoc, "/auth") || strings.Contains(lowerLoc, "/signup")) && 
				!strings.Contains(lowerLoc, "/api/auth-handshake") && 
				!strings.Contains(lowerLoc, "/access") {
				
				log.Printf("[SWAP] 🔄 Upstream redirected to login page: %s. Swapping account!", loc)
				if db != nil && resp.Request != nil {
					sessionCookie, errC := resp.Request.Cookie("sem_session")
					if errC == nil && sessionCookie != nil && sessionCookie.Value != "" {
						// Check swap count to prevent loops
						swapCount := 0
						if swapCookie, errS := resp.Request.Cookie("sem_swap_count"); errS == nil {
							fmt.Sscanf(swapCookie.Value, "%d", &swapCount)
						}

						if swapCount >= 3 {
							log.Printf("[SWAP] ⚠️ Loop protection active on redirect! Already swapped %d times. Let login page display.", swapCount)
						} else {
							var currentAssignedID sql.NullInt64
							var username string
							var sessionWebsiteID int
							errSession := db.QueryRow("SELECT username, website_id, assigned_account_id FROM ahrefs_sessions WHERE session_token = ?", sessionCookie.Value).Scan(&username, &sessionWebsiteID, &currentAssignedID)

							if errSession == nil {
								var nextAcc Account
								var errSelect error
								if currentAssignedID.Valid {
									errSelect = db.QueryRow("SELECT id, name, cookie, user_agent FROM ahrefs_accounts WHERE website_id = ? AND status = 'active' AND id != ? ORDER BY last_used_at ASC LIMIT 1", sessionWebsiteID, currentAssignedID.Int64).Scan(&nextAcc.ID, &nextAcc.Name, &nextAcc.Cookie, &nextAcc.UserAgent)
								}
								if !currentAssignedID.Valid || errSelect != nil {
									errSelect = db.QueryRow("SELECT id, name, cookie, user_agent FROM ahrefs_accounts WHERE website_id = ? AND status = 'active' ORDER BY last_used_at ASC LIMIT 1", sessionWebsiteID).Scan(&nextAcc.ID, &nextAcc.Name, &nextAcc.Cookie, &nextAcc.UserAgent)
								}

								if errSelect == nil {
									log.Printf("[SWAP] 🔄 Login redirect detected! Swapping from account ID %v to account ID %d (%s) for session %s (Swap count: %d)", 
										currentAssignedID.Int64, nextAcc.ID, nextAcc.Name, sessionCookie.Value, swapCount+1)

									_, _ = db.Exec("UPDATE ahrefs_sessions SET assigned_account_id = ? WHERE session_token = ? AND website_id = ?", nextAcc.ID, sessionCookie.Value, sessionWebsiteID)
									
									var fromAccName string
									if currentAssignedID.Valid {
										_ = db.QueryRow("SELECT name FROM ahrefs_accounts WHERE id = ?", currentAssignedID.Int64).Scan(&fromAccName)
										_, _ = db.Exec("UPDATE ahrefs_accounts SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", currentAssignedID.Int64)
									}
									_, _ = db.Exec("UPDATE ahrefs_accounts SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", nextAcc.ID)

									// Log the switch event to central ahrefs_switch_logs
									_, _ = db.Exec("INSERT INTO ahrefs_switch_logs (website_id, session_token, username, from_account_id, from_account_name, to_account_id, to_account_name, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
										sessionWebsiteID, sessionCookie.Value, username, currentAssignedID, fromAccName, nextAcc.ID, nextAcc.Name, "Semrush auth redirect detected")

									// Increment and set swap count cookie
									newSwapCountCookie := &http.Cookie{
										Name:     "sem_swap_count",
										Value:    fmt.Sprintf("%d", swapCount+1),
										Path:     "/",
										MaxAge:   10,
										HttpOnly: true,
									}
									resp.Header.Add("Set-Cookie", newSwapCountCookie.String())

									// Return gorgeous loading screen that reloads/redirects to "/"
									htmlContent := renderSwappingPage("/")
									resp.StatusCode = http.StatusOK
									resp.Header.Set("Content-Type", "text/html; charset=utf-8")
									resp.Body = io.NopCloser(strings.NewReader(htmlContent))
									resp.ContentLength = int64(len(htmlContent))
									resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(htmlContent)))
									resp.Header.Del("Content-Encoding")
									resp.Header.Del("Location")
									return nil
								} else {
									log.Printf("[SWAP] ⚠️ Login redirect detected but no alternative active account found in DB for website_id = %d: %v", sessionWebsiteID, errSelect)
								}
							}
						}
					}
				}
			}

			replacedLoc := strings.ReplaceAll(loc, "https://www.semrush.com", proxyScheme+"://"+proxyHost)
			replacedLoc = strings.ReplaceAll(replacedLoc, "https://semrush.com", proxyScheme+"://"+proxyHost)
			replacedLoc = strings.ReplaceAll(replacedLoc, "https://secure.semrush.com", proxyScheme+"://"+proxyHost+"/secure-proxy")
			replacedLoc = strings.ReplaceAll(replacedLoc, "https://static.semrush.com", proxyScheme+"://"+proxyHost+"/static-proxy")
			replacedLoc = strings.ReplaceAll(replacedLoc, "https://cdn.semrush.com", proxyScheme+"://"+proxyHost+"/cdn-proxy")
			replacedLoc = strings.ReplaceAll(replacedLoc, "https://ai-visibility-index.semrush.com", proxyScheme+"://"+proxyHost+"/ai-proxy")
			if loc != "" {
				resp.Header.Set("Location", replacedLoc)
			}
		}

		if rl := getRequestLogger(resp.Request.Context()); rl != nil {
			rl.status = resp.StatusCode
		}

		if cfg.LocalTestMode {
			resp.Header.Del("Set-Cookie")
		} else {
			for _, cookie := range resp.Cookies() {
				cookie.Domain = ""
				cookie.Secure = false
			}
		}

		contentType := resp.Header.Get("Content-Type")
		isText := strings.Contains(contentType, "text") || 
			strings.Contains(contentType, "javascript") || 
			strings.Contains(contentType, "json") || 
			strings.Contains(contentType, "xml")

		if isText {
			encoding := resp.Header.Get("Content-Encoding")
			isGzip := strings.EqualFold(encoding, "gzip")

			var reader io.Reader
			var err error
			if isGzip {
				gzipReader, err := gzip.NewReader(resp.Body)
				if err != nil {
					return err
				}
				defer gzipReader.Close()
				reader = gzipReader
			} else {
				reader = resp.Body
			}

			bodyBytes, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			resp.Body.Close()

			bodyStr := string(bodyBytes)

			if debugEnabled(cfg) && resp.Request != nil {
				peekLen := len(bodyBytes)
				if peekLen > 240 {
					peekLen = 240
				}
				if hint := bodyDiagnostic(resp.StatusCode, bodyBytes[:peekLen]); hint != "" {
					if rl := getRequestLogger(resp.Request.Context()); rl != nil {
						rl.bodyHint = hint
					}
				}
			}

			// Detect limits, register prompts, and upgrade buttons in HTML response
			isHTML := strings.Contains(contentType, "text/html")
			if isHTML && db != nil {
				if detectLimitOrLogout(bodyStr) {
					if resp.Request != nil {
						sessionCookie, errC := resp.Request.Cookie("sem_session")
						if errC == nil && sessionCookie != nil && sessionCookie.Value != "" {
							// Check swap count to prevent loops
							swapCount := 0
							if swapCookie, errS := resp.Request.Cookie("sem_swap_count"); errS == nil {
								fmt.Sscanf(swapCookie.Value, "%d", &swapCount)
							}

							if swapCount >= 3 {
								log.Printf("[SWAP] ⚠️ Loop protection active! Already swapped %d times. Stopping rotation to avoid ERR_TOO_MANY_REDIRECTS.", swapCount)
								// Let the page render so the user sees the actual limit/login page
							} else {
								var currentAssignedID sql.NullInt64
								var username string
								var sessionWebsiteID int
								errSession := db.QueryRow("SELECT username, website_id, assigned_account_id FROM ahrefs_sessions WHERE session_token = ?", sessionCookie.Value).Scan(&username, &sessionWebsiteID, &currentAssignedID)

								if errSession == nil {
									var nextAcc Account
									var errSelect error
									if currentAssignedID.Valid {
										errSelect = db.QueryRow("SELECT id, name, cookie, user_agent FROM ahrefs_accounts WHERE website_id = ? AND status = 'active' AND id != ? ORDER BY last_used_at ASC LIMIT 1", sessionWebsiteID, currentAssignedID.Int64).Scan(&nextAcc.ID, &nextAcc.Name, &nextAcc.Cookie, &nextAcc.UserAgent)
									}
									if !currentAssignedID.Valid || errSelect != nil {
										errSelect = db.QueryRow("SELECT id, name, cookie, user_agent FROM ahrefs_accounts WHERE website_id = ? AND status = 'active' ORDER BY last_used_at ASC LIMIT 1", sessionWebsiteID).Scan(&nextAcc.ID, &nextAcc.Name, &nextAcc.Cookie, &nextAcc.UserAgent)
									}

									if errSelect == nil {
										log.Printf("[SWAP] 🔄 Limit/Logout detected in response body! Swapping from account ID %v to account ID %d (%s) for session %s (Swap count: %d)", 
											currentAssignedID.Int64, nextAcc.ID, nextAcc.Name, sessionCookie.Value, swapCount+1)

										_, _ = db.Exec("UPDATE ahrefs_sessions SET assigned_account_id = ? WHERE session_token = ? AND website_id = ?", nextAcc.ID, sessionCookie.Value, sessionWebsiteID)
										
										var fromAccName string
										if currentAssignedID.Valid {
											_ = db.QueryRow("SELECT name FROM ahrefs_accounts WHERE id = ?", currentAssignedID.Int64).Scan(&fromAccName)
											_, _ = db.Exec("UPDATE ahrefs_accounts SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", currentAssignedID.Int64)
										}
										_, _ = db.Exec("UPDATE ahrefs_accounts SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", nextAcc.ID)

										// Log the switch event to central ahrefs_switch_logs
										_, _ = db.Exec("INSERT INTO ahrefs_switch_logs (website_id, session_token, username, from_account_id, from_account_name, to_account_id, to_account_name, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
											sessionWebsiteID, sessionCookie.Value, username, currentAssignedID, fromAccName, nextAcc.ID, nextAcc.Name, "Semrush free/limit detection")

										// Return transparent loading screen back to the exact current page path + query
										redirectURL := resp.Request.URL.Path
										if resp.Request.URL.RawQuery != "" {
											redirectURL += "?" + resp.Request.URL.RawQuery
										}
										
										// Increment and set swap count cookie
										newSwapCountCookie := &http.Cookie{
											Name:     "sem_swap_count",
											Value:    fmt.Sprintf("%d", swapCount+1),
											Path:     "/",
											MaxAge:   10,
											HttpOnly: true,
										}
										resp.Header.Add("Set-Cookie", newSwapCountCookie.String())

										// Return gorgeous loading screen that reloads/redirects to redirectURL
										htmlContent := renderSwappingPage(redirectURL)
										resp.StatusCode = http.StatusOK
										resp.Header.Set("Content-Type", "text/html; charset=utf-8")
										resp.Body = io.NopCloser(strings.NewReader(htmlContent))
										resp.ContentLength = int64(len(htmlContent))
										resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(htmlContent)))
										resp.Header.Del("Content-Encoding")
										resp.Header.Del("Location")
										return nil
									} else {
										log.Printf("[SWAP] ⚠️ Limit/Logout detected but no alternative active account found in DB for website_id = %d: %v", sessionWebsiteID, errSelect)
									}
								}
							}
						}
					}
				} else {
					// Clear the swap count cookie on successful page load
					if resp.Request != nil {
						if _, errS := resp.Request.Cookie("sem_swap_count"); errS == nil {
							clearCookie := &http.Cookie{
								Name:     "sem_swap_count",
								Value:    "",
								Path:     "/",
								MaxAge:   -1,
								HttpOnly: true,
							}
							resp.Header.Add("Set-Cookie", clearCookie.String())
						}
					}
				}
			}

			proxySchemeHost := proxyScheme + "://" + proxyHost

			bodyStr = strings.ReplaceAll(bodyStr, "https://www.semrush.com", proxySchemeHost)
			bodyStr = strings.ReplaceAll(bodyStr, "https://semrush.com", proxySchemeHost)
			bodyStr = strings.ReplaceAll(bodyStr, "https://static.semrush.com", proxySchemeHost+"/static-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "https://secure.semrush.com", proxySchemeHost+"/secure-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "https://cdn.semrush.com", proxySchemeHost+"/cdn-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "https://ai-visibility-index.semrush.com", proxySchemeHost+"/ai-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "//static.semrush.com", "//"+proxyHost+"/static-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "//secure.semrush.com", "//"+proxyHost+"/secure-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "//cdn.semrush.com", "//"+proxyHost+"/cdn-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, "//ai-visibility-index.semrush.com", "//"+proxyHost+"/ai-proxy")
			bodyStr = strings.ReplaceAll(bodyStr, `\/static.semrush.com\/`, `\/`+proxyHost+`\/static-proxy\/`)
			bodyStr = strings.ReplaceAll(bodyStr, `\/secure.semrush.com\/`, `\/`+proxyHost+`\/secure-proxy\/`)
			bodyStr = strings.ReplaceAll(bodyStr, `\/cdn.semrush.com\/`, `\/`+proxyHost+`\/cdn-proxy\/`)
			bodyStr = strings.ReplaceAll(bodyStr, `\/ai-visibility-index.semrush.com\/`, `\/`+proxyHost+`\/ai-proxy\/`)
			bodyStr = strings.ReplaceAll(bodyStr, `\/www.semrush.com\/`, `\/`+proxyHost+`\/`)
			bodyStr = strings.ReplaceAll(bodyStr, `/www.semrush.com`, `/`+proxyHost)
			bodyStr = integrityRegex.ReplaceAllString(bodyStr, "")

			if strings.Contains(contentType, "javascript") {
				bodyStr = strings.ReplaceAll(bodyStr, ".integrity=", "._ntegrity=")
				bodyStr = webpackAttrRegex.ReplaceAllString(bodyStr, `"_ntegrity":`)
				bodyStr = webpackSetAttrRegex.ReplaceAllString(bodyStr, `setAttribute("_ntegrity"`)
			}

			if strings.Contains(contentType, "text/html") {
				lowerBody := strings.ToLower(bodyStr)
				isMultiloginPage := strings.Contains(lowerBody, "too many active sessions") ||
					(resp.Request != nil && strings.Contains(strings.ToLower(resp.Request.URL.Path), "multilogin"))

				if isMultiloginPage {
					bodyStr = strings.Replace(bodyStr, "</head>", multiloginAutoScript+"</head>", 1)
					if rl := getRequestLogger(resp.Request.Context()); rl != nil {
						rl.bodyHint = "MULTILOGIN_PAGE"
						rl.category = "MULTILOGIN"
					}
				} else {
				customExportJS := ""
				if customExportJSBytes, err := os.ReadFile("export-tool.js"); err == nil {
					customExportJS = string(customExportJSBytes)
				}

				localHostFix := buildLocalHostFix(cfg)
				sessionCookies := loadCookies(cfg)
				sessionFP := semrushDeviceFingerprint(cfg, sessionCookies)
				sessionBootstrap := buildSemrushSessionBootstrapJS(sessionFP)
				grpcWebFix := buildGrpcWebFix(cookieValueFromString(sessionCookies, "SSO-JWT"))
				blockScript := `<script>
					` + localHostFix + grpcWebFix + sessionBootstrap + `

					window.intercomSettings = { app_id: "" };
					window.Intercom = function() { return false; };
					window.Hotjar = function() { return false; };
					window.hj = function() { return false; };

					function shouldBlockUrl(u) {
						return u.includes("block-sentry") || u.includes("intercom.io") || u.includes("hotjar") || u.includes("_sentry");
					}

					function proxyUrl(url) {
						if (typeof url !== "string") return url;
						let u = url.trim();
						if (u.startsWith("https://ai-visibility-index.semrush.com")) {
							return u.replace("https://ai-visibility-index.semrush.com", REAL_PROXY_ORIGIN + "/ai-proxy");
						}
						if (u.startsWith("http://ai-visibility-index.semrush.com")) {
							return u.replace("http://ai-visibility-index.semrush.com", REAL_PROXY_ORIGIN + "/ai-proxy");
						}
						if (u.startsWith("//ai-visibility-index.semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//ai-visibility-index.semrush.com", "//" + REAL_PROXY_HOST + "/ai-proxy");
						}
						if (u.startsWith("https://cdn.semrush.com")) {
							return u.replace("https://cdn.semrush.com", REAL_PROXY_ORIGIN + "/cdn-proxy");
						}
						if (u.startsWith("http://cdn.semrush.com")) {
							return u.replace("http://cdn.semrush.com", REAL_PROXY_ORIGIN + "/cdn-proxy");
						}
						if (u.startsWith("//cdn.semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//cdn.semrush.com", "//" + REAL_PROXY_HOST + "/cdn-proxy");
						}
						if (u.startsWith("https://static.semrush.com")) {
							return u.replace("https://static.semrush.com", REAL_PROXY_ORIGIN + "/static-proxy");
						}
						if (u.startsWith("http://static.semrush.com")) {
							return u.replace("http://static.semrush.com", REAL_PROXY_ORIGIN + "/static-proxy");
						}
						if (u.startsWith("//static.semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//static.semrush.com", "//" + REAL_PROXY_HOST + "/static-proxy");
						}
						if (u.startsWith("https://secure.semrush.com")) {
							return u.replace("https://secure.semrush.com", REAL_PROXY_ORIGIN + "/secure-proxy");
						}
						if (u.startsWith("http://secure.semrush.com")) {
							return u.replace("http://secure.semrush.com", REAL_PROXY_ORIGIN + "/secure-proxy");
						}
						if (u.startsWith("//secure.semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//secure.semrush.com", "//" + REAL_PROXY_HOST + "/secure-proxy");
						}
						if (u.startsWith("https://www.semrush.com")) {
							return u.replace("https://www.semrush.com", REAL_PROXY_ORIGIN);
						}
						if (u.startsWith("https://semrush.com")) {
							return u.replace("https://semrush.com", REAL_PROXY_ORIGIN);
						}
						if (u.startsWith("http://www.semrush.com")) {
							return u.replace("http://www.semrush.com", REAL_PROXY_ORIGIN);
						}
						if (u.startsWith("http://semrush.com")) {
							return u.replace("http://semrush.com", REAL_PROXY_ORIGIN);
						}
						if (u.startsWith("//www.semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//www.semrush.com", "//" + REAL_PROXY_HOST);
						}
						if (u.startsWith("//semrush.com")) {
							return REAL_PROXY_PROTOCOL + u.replace("//semrush.com", "//" + REAL_PROXY_HOST);
						}
						return url;
					}
					
					const __originalFetch = window.fetch;
					window.fetch = function(input, init) {
						if (input) {
							if (typeof input === "string") {
								const proxied = proxyUrl(input);
								if (proxied !== input) {
									if (shouldBlockUrl(proxied)) {
										return Promise.resolve(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
									}
									input = proxied;
								}
							} else if (input instanceof Request) {
								const originalUrl = input.url;
								const proxied = proxyUrl(originalUrl);
								if (proxied !== originalUrl) {
									if (shouldBlockUrl(proxied)) {
										return Promise.resolve(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
									}
									const newRequest = new Request(proxied, {
										method: input.method,
										headers: input.headers,
										body: input.body,
										mode: input.mode,
										credentials: input.credentials,
										cache: input.cache,
										redirect: input.redirect,
										referrer: input.referrer,
										integrity: input.integrity,
										keepalive: input.keepalive,
										signal: input.signal
									});
									input = newRequest;
								}
							} else if (input instanceof URL) {
								const urlStr = input.toString();
								const proxied = proxyUrl(urlStr);
								if (proxied !== urlStr) {
									input = new URL(proxied);
								}
							}
						}
						return __originalFetch.call(this, input, init);
					};
					
					const __originalSendBeacon = window.navigator.sendBeacon;
					window.navigator.sendBeacon = function(url, data) {
						if (typeof url === "string") {
							const proxied = proxyUrl(url);
							if (shouldBlockUrl(proxied)) {
								return true;
							}
							url = proxied;
						}
						return __originalSendBeacon.call(window.navigator, url, data);
					};
					
					const __originalXHR = window.XMLHttpRequest.prototype.open;
					window.XMLHttpRequest.prototype.open = function(method, url, async, user, password) {
						if (url) {
							let urlStr = (url instanceof URL) ? url.toString() : String(url);
							// gRPC widget APIs — force through proxy even if app-bootstrap cached old origin
							if (urlStr.indexOf("/seo/api/") !== -1 || urlStr.indexOf("semrush.com/seo/api") !== -1) {
								urlStr = proxyUrl(urlStr);
							} else {
								const proxied = proxyUrl(urlStr);
								if (proxied !== urlStr) urlStr = proxied;
							}
							if (shouldBlockUrl(urlStr)) {
								url = "data:application/json,{}";
							} else {
								url = urlStr;
							}
						}
						// Preserve argument arity — passing undefined as async forces sync XHR and breaks gRPC-web responseType.
						var argc = arguments.length;
						if (argc === 2) return __originalXHR.call(this, method, url);
						if (argc === 3) return __originalXHR.call(this, method, url, async);
						if (argc === 4) return __originalXHR.call(this, method, url, async, user);
						return __originalXHR.call(this, method, url, async, user, password);
					};

					function hideCookieConsentBanners() {
						const selectors = [
							"[id*='cookie' i]", "[class*='cookie' i]", "[id*='consent' i]", "[class*='consent' i]",
							".ch2", ".ch2-container", "#ch2-dialog", "[data-region='c1']",
							"div[style*='z-index: 2147483647']", "div[style*='z-index:99999999']",
							"#onetrust-banner-sdk", ".ot-sdk-container"
						];
						selectors.forEach(sel => {
							try {
								document.querySelectorAll(sel).forEach(el => {
									if (el.id === "semrush-export-container" || el.closest("#semrush-export-container")) return;
									if (el.tagName === "BODY" || el.tagName === "HTML" || el.id === "root" || el.id === "app") return;
									el.style.setProperty("display", "none", "important");
									el.style.setProperty("visibility", "hidden", "important");
									el.style.setProperty("opacity", "0", "important");
									el.style.setProperty("pointer-events", "none", "important");
								});
							} catch(e){}
						});
						
						if (!document.body) return;
						const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, null, false);
						let node;
						while (node = walk.nextNode()) {
							if (node.nodeValue.includes("We use cookies to run our website") || node.nodeValue.includes("Cookie Policy")) {
								let parent = node.parentElement;
								for (let i = 0; i < 5; i++) {
									if (parent && parent.tagName !== "BODY" && parent.tagName !== "HTML") {
										parent.style.setProperty("display", "none", "important");
										parent.style.setProperty("visibility", "hidden", "important");
										parent.style.setProperty("opacity", "0", "important");
										parent.style.setProperty("pointer-events", "none", "important");
										parent = parent.parentElement;
									}
								}
							}
						}
					}
					
					const observer = new MutationObserver((mutations) => {
						hideCookieConsentBanners();
					});
					observer.observe(document.documentElement, { childList: true, subtree: true });
					
					window.addEventListener("load", hideCookieConsentBanners);
					setInterval(hideCookieConsentBanners, 500);
				</script>
				<style>
					.ch2, .ch2-container, #ch2-dialog, [data-region="c1"], #ch2-dialog-title, #ch2-dialog-description { 
						display: none !important; 
						visibility: hidden !important; 
						opacity: 0 !important; 
						pointer-events: none !important; 
					}
				</style>`
				exportScript := ""
				if customExportJS != "" {
					exportScript = "<script>\n" + customExportJS + "\n</script>"
				}

				bodyStr = strings.ReplaceAll(bodyStr, "<head>", "<head>"+"\n"+blockScript+"\n"+exportScript)
				}
			}

			modifiedBytes := []byte(bodyStr)

			if isGzip {
				var buf bytes.Buffer
				gzipWriter := gzip.NewWriter(&buf)
				if _, err := gzipWriter.Write(modifiedBytes); err != nil {
					return err
				}
				gzipWriter.Close()
				resp.Body = io.NopCloser(&buf)
				resp.ContentLength = int64(buf.Len())
				resp.Header.Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
			} else {
				resp.Body = io.NopCloser(bytes.NewBuffer(modifiedBytes))
				resp.ContentLength = int64(len(modifiedBytes))
				resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBytes)))
			}
			if resp.StatusCode == http.StatusOK && resp.Request != nil && resp.Request.Method == "GET" && isCacheablePath(resp.Request.URL.Path) {
				cacheMutex.Lock()
				assetCache[resp.Request.URL.Path] = CachedResponse{
					StatusCode: resp.StatusCode,
					Header:     resp.Header,
					Body:       modifiedBytes,
				}
				cacheMutex.Unlock()
			}
		} else {
			if resp.StatusCode == http.StatusOK && resp.Request != nil && resp.Request.Method == "GET" && isCacheablePath(resp.Request.URL.Path) {
				bodyBytes, err := io.ReadAll(resp.Body)
				if err == nil {
					resp.Body.Close()
					resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
					
					cacheMutex.Lock()
					assetCache[resp.Request.URL.Path] = CachedResponse{
						StatusCode: resp.StatusCode,
						Header:     resp.Header,
						Body:       bodyBytes,
					}
					cacheMutex.Unlock()
				}
			}
		}

		return nil
	}

	addr := ":" + cfg.Port
	if cfg.BindLocalhost {
		addr = "127.0.0.1:" + cfg.Port
	}
	listenDisplay := addr
	if strings.HasPrefix(addr, ":") {
		listenDisplay = "127.0.0.1" + addr
	}
	log.Printf("╔══════════════════════════════════════════════════╗")
	log.Printf("║  🚀 SEMrush NEW Go Proxy — www.semrush.com       ║")
	log.Printf("║  Local URL: http://%-28s ║", listenDisplay)
	log.Printf("║  Target   : %s                        ║", cfg.TargetURL)
	log.Printf("║  CDN Proxy: /static-proxy/ /secure-proxy/ /cdn-proxy/ /ai-proxy/ ║")
	if cfg.LocalTestMode {
		log.Printf("║  Mode     : LOCAL TEST (cookie.txt) ✅            ║")
	}
	log.Printf("║  Hot-Reload: %s & cookie.txt ✅                 ║", getConfigFile())
	log.Printf("╚══════════════════════════════════════════════════╝")
	logStartupDiagnostics(cfg)
	startStatsReporter(cfg)

	// Pre-activate Semrush session so first page load skips multilogin wall.
	if cfg.LocalTestMode {
		if ck := loadCookies(cfg); ck != "" {
			ensureSemrushSessionActivated(cfg, ck, true)
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg = loadConfig()

		rl := &requestLogger{
			start:      time.Now(),
			reqHost:    r.Host,
			method:     r.Method,
			path:       r.URL.Path,
			query:      r.URL.RawQuery,
			origin:     r.Header.Get("Origin"),
			clientIP:   realClientIP(r),
			category:   classifyPath(r.URL.Path),
			cookieInfo: summarizeSemrushCookies(r.Header.Get("Cookie"), ""),
		}
		defer rl.finish(cfg)
		r = r.WithContext(context.WithValue(r.Context(), proxyLogKey, rl))
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// 00. Global Path Blocking Security (Domain-Independent)
		// Prevent users from accessing sensitive profile/billing/subscription URLs
		requestPath := strings.ToLower(r.URL.Path)
		blockedPaths := []string{
			"/accounts/profile",
			"/accounts/subscription-info",
			"/accounts/notifications",
			"/accounts/queries",
			"/accounts/activities",
			"/accounts/tokens",
			"/company/beta-tester-club",
			"/enterprise",
		}
		for _, blocked := range blockedPaths {
			if requestPath == blocked || strings.HasPrefix(requestPath, blocked+"/") {
				log.Printf("[BLOCK] 🛡️ Global block triggered for sensitive path: %s", r.URL.Path)
				// Relative redirect back to Semrush home dashboard root (works perfectly for all reseller domains!)
				http.Redirect(sr, r, "/", http.StatusFound)
				rl.status = http.StatusFound
				return
			}
		}

		// 0. Server-to-Server Auth Handshake API
		if r.URL.Path == "/api/auth-handshake" {
			if r.Method != http.MethodPost {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}

			var payload struct {
				Username      string        `json:"username"`
				ProductIDsRaw []interface{} `json:"product_ids"`
				ClientIP      string        `json:"client_ip"`
				Timestamp     int64         `json:"timestamp"`
				Signature     string        `json:"signature"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				log.Printf("[HANDSHAKE] Bad JSON payload: %v", err)
				http.Error(w, "Bad Request: Invalid JSON", http.StatusBadRequest)
				return
			}

			if payload.Username == "" || payload.ClientIP == "" || payload.Signature == "" {
				http.Error(w, "Bad Request: Missing required fields", http.StatusBadRequest)
				return
			}

			// Determine scheme and host dynamically
			currentHost := normalizeHost(r.Host)
			currentScheme := "http"
			if r.TLS != nil {
				currentScheme = "https"
			} else if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
				currentScheme = proto
			} else if cfg.PublicScheme != "" {
				currentScheme = cfg.PublicScheme
			}
			if currentHost == "" {
				currentHost = normalizeHost(cfg.PublicHost)
			}

			requestWebsiteID := lookupWebsiteIDByHost(currentHost)
			if requestWebsiteID <= 0 {
				log.Printf("[HANDSHAKE] ❌ Unknown host '%s'", currentHost)
				http.Error(w, "Forbidden: Unknown domain", http.StatusForbidden)
				return
			}

			// Validate signature using secret key from database (or config fallback)
			var dbSecretKey string
			err := db.QueryRow("SELECT secret_key FROM ahrefs_websites WHERE id = ?", requestWebsiteID).Scan(&dbSecretKey)
			if err != nil {
				// Default Semrush Handshake key fallback
				dbSecretKey = "toolsmandi_semrush_secret_xyz123"
			}

			// HMAC-SHA256 signature check
			mac := hmac.New(sha256.New, []byte(dbSecretKey))
			mac.Write([]byte(fmt.Sprintf("%s:%d", payload.Username, payload.Timestamp)))
			expectedSig := hex.EncodeToString(mac.Sum(nil))

			if !hmac.Equal([]byte(payload.Signature), []byte(expectedSig)) {
				log.Printf("[HANDSHAKE] ❌ HMAC verification failed for user: %s", payload.Username)
				http.Error(w, "Forbidden: Invalid signature", http.StatusForbidden)
				return
			}

			// Reject requests older than 5 minutes
			if time.Now().Unix()-payload.Timestamp > 300 {
				log.Printf("[HANDSHAKE] ❌ Stale request rejected: %s", payload.Username)
				http.Error(w, "Forbidden: Request expired", http.StatusForbidden)
				return
			}

			if db == nil {
				http.Error(w, "Service Unavailable: DB Offline", http.StatusServiceUnavailable)
				return
			}

			// Generate One-Time Token (OTT)
			ottBytes := make([]byte, 32)
			if _, err := rand.Read(ottBytes); err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			ott := hex.EncodeToString(ottBytes)
			expiresAt := time.Now().Add(2 * time.Minute)

			// Insert OTT into local database under the resolved requestWebsiteID
			_, err = db.Exec(
				"INSERT INTO ahrefs_tokens (website_id, token, username, client_ip, expires_at) VALUES (?, ?, ?, ?, ?)",
				requestWebsiteID, ott, payload.Username, payload.ClientIP, expiresAt,
			)
			if err != nil {
				log.Printf("[HANDSHAKE] ❌ DB Error inserting OTT: %v", err)
				http.Error(w, "Internal Database Error", http.StatusInternalServerError)
				return
			}

			// Return secure handshake redirect URL
			redirectURL := fmt.Sprintf("%s://%s/access?user=%s&token=%s",
				currentScheme, currentHost, url.QueryEscape(payload.Username), ott)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"redirect_url": redirectURL,
			})
			return
		}

		// 1. OTT access token handshake
		if r.URL.Path == "/access" {
			username := r.URL.Query().Get("user")
			ott := r.URL.Query().Get("token")

			if username == "" || ott == "" {
				http.Error(w, "Bad Request: Missing user or token", http.StatusBadRequest)
				return
			}

			if db == nil {
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				return
			}

			var storedUsername, storedIP string
			var expiresAt time.Time
			var tokenID, tokenWebsiteID int
			err = db.QueryRow(
				"SELECT id, website_id, username, client_ip, expires_at FROM ahrefs_tokens WHERE token = ?",
				ott,
			).Scan(&tokenID, &tokenWebsiteID, &storedUsername, &storedIP, &expiresAt)

			if err == sql.ErrNoRows {
				log.Printf("[ACCESS] ❌ OTT not found for token: %s", ott)
				renderAccessDeniedPage(w)
				return
			} else if err != nil {
				log.Printf("[ACCESS] DB error on OTT lookup: %v", err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}

			// Dynamically synchronize the website_id from the authorized token
			accessWebsiteID := tokenWebsiteID

			// Delete OTT
			_, _ = db.Exec("DELETE FROM ahrefs_tokens WHERE id = ?", tokenID)

			if time.Now().After(expiresAt) {
				log.Printf("[ACCESS] ❌ Expired OTT for user: %s (expired at %v)", username, expiresAt)
				renderAccessDeniedPage(w)
				return
			}

			if storedUsername != username {
				log.Printf("[ACCESS] ❌ Username mismatch: token was for '%s', got '%s'", storedUsername, username)
				renderAccessDeniedPage(w)
				return
			}

			if !validateRequestHostMatchesWebsite(r, tokenWebsiteID) {
				log.Printf("[ACCESS] ❌ Host/domain mismatch for website_id=%d host=%s", tokenWebsiteID, r.Host)
				renderAccessDeniedPage(w)
				return
			}

			if storedIP != "" && storedIP != realClientIP(r) {
				log.Printf("[ACCESS] ❌ OTT IP mismatch for user %s (stored=%s got=%s)", username, storedIP, realClientIP(r))
				renderAccessDeniedPage(w)
				return
			}

			// Automatically expire custom limits that have passed their expiration date
			_, _ = db.Exec(`
				UPDATE ahrefs_users u
				JOIN ahrefs_websites w ON u.website_id = w.id
				SET u.credit_limit = COALESCE(w.default_credit_limit, 50),
					u.export_limit = COALESCE(w.default_export_limit, 100000),
					u.custom_limit_expire_at = NULL
				WHERE u.custom_limit_expire_at IS NOT NULL AND u.custom_limit_expire_at < NOW()
			`)

			var dbStatus string
			err = db.QueryRow("SELECT status FROM ahrefs_users WHERE username = ? AND website_id = ?", username, accessWebsiteID).Scan(&dbStatus)
			if err == sql.ErrNoRows {
				_, _ = db.Exec("INSERT INTO ahrefs_users (username, website_id, credit_limit, export_limit) VALUES (?, ?, 50, 100000)", username, accessWebsiteID)
			} else if dbStatus == "suspended" {
				http.Error(w, "Account Suspended. Contact support.", http.StatusForbidden)
				return
			}

			sessionToken := generateSessionToken()
			sessionDuration := 30
			var dbSessionDuration int
			if errDb := db.QueryRow("SELECT session_duration FROM ahrefs_websites WHERE id = ?", accessWebsiteID).Scan(&dbSessionDuration); errDb == nil && dbSessionDuration > 0 {
				sessionDuration = dbSessionDuration
			}
			sessionExpires := time.Now().Add(time.Duration(sessionDuration) * time.Minute)

			// Single session enforcement
			_, _ = db.Exec("DELETE FROM ahrefs_sessions WHERE username = ? AND website_id = ?", username, accessWebsiteID)

			var assignedAccountID sql.NullInt64
			if initialAcc, errAcc := selectActiveAccount(accessWebsiteID); errAcc == nil {
				assignedAccountID.Int64 = int64(initialAcc.ID)
				assignedAccountID.Valid = true
			}

			_, err = db.Exec(
				"INSERT INTO ahrefs_sessions (session_token, username, client_ip, expires_at, website_id, assigned_account_id) VALUES (?, ?, ?, ?, ?, ?)",
				sessionToken, username, realClientIP(r), sessionExpires, accessWebsiteID, assignedAccountID,
			)
			if err != nil {
				log.Printf("[ACCESS] ❌ DB Error inserting session: %v", err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}

			userAgentStr := r.Header.Get("User-Agent")
			_, _ = db.Exec("INSERT INTO ahrefs_login_logs (website_id, username, client_ip, user_agent) VALUES (?, ?, ?, ?)", accessWebsiteID, username, realClientIP(r), userAgentStr)

			http.SetCookie(w, &http.Cookie{
				Name:     "sem_session",
				Value:    sessionToken,
				Path:     "/",
				Expires:  sessionExpires,
				HttpOnly: true,
				Secure:   cfg.PublicScheme == "https",
				SameSite: http.SameSiteLaxMode,
			})

			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		// 2. Session verification & active sliding window (skip for static assets)
		isStatic := strings.HasPrefix(r.URL.Path, "/static-proxy/") || strings.HasPrefix(r.URL.Path, "/secure-proxy/") || strings.HasPrefix(r.URL.Path, "/cdn-proxy/") || strings.HasPrefix(r.URL.Path, "/ai-proxy/")
		if !isStatic && db != nil && !cfg.LocalTestMode {
			var isAuthed bool
			cookie, errC := r.Cookie("sem_session")
			if errC == nil && cookie.Value != "" {
				var username string
				var expiresAt time.Time
				var sessionWebsiteID int
				errS := db.QueryRow("SELECT username, expires_at, website_id FROM ahrefs_sessions WHERE session_token = ?", cookie.Value).Scan(&username, &expiresAt, &sessionWebsiteID)
				if errS == nil && time.Now().Before(expiresAt) {
					if !validateRequestHostMatchesWebsite(r, sessionWebsiteID) {
						log.Printf("[AUTH] ❌ Session host mismatch for website_id=%d host=%s", sessionWebsiteID, r.Host)
					} else {
					isAuthed = true

					r = r.WithContext(withTenantWebsiteID(r.Context(), sessionWebsiteID))

					// Extend session sliding window
					sessionDuration := 30
					var dbSessionDuration int
					if errDb := db.QueryRow("SELECT session_duration FROM ahrefs_websites WHERE id = ?", sessionWebsiteID).Scan(&dbSessionDuration); errDb == nil && dbSessionDuration > 0 {
						sessionDuration = dbSessionDuration
					}
					newExpires := time.Now().Add(time.Duration(sessionDuration) * time.Minute)
					_, _ = db.Exec("UPDATE ahrefs_sessions SET expires_at = ? WHERE session_token = ?", newExpires, cookie.Value)
					}
				} else {
					log.Printf("[AUTH] ❌ Session validation failed for token '%s': ErrS=%v, Expired=%v (Path: %s)", cookie.Value, errS, time.Now().After(expiresAt), r.URL.Path)
				}
			} else {
				log.Printf("[AUTH] ❌ No active sem_session cookie found in request (Path: %s)", r.URL.Path)
			}

			if !isAuthed {
				renderAccessDeniedPage(w)
				return
			}
		}

		// 3. Cache and serve
		if r.Method == "GET" && isCacheablePath(r.URL.Path) {
			cacheMutex.RLock()
			cached, found := assetCache[r.URL.Path]
			cacheMutex.RUnlock()
			if found {
				for k, values := range cached.Header {
					for _, v := range values {
						w.Header().Add(k, v)
					}
				}
				w.Header().Set("X-Proxy-Cache", "HIT")
				w.WriteHeader(cached.StatusCode)
				w.Write(cached.Body)
				return
			}
		}
		proxy.ServeHTTP(sr, r)
		if rl.status == 0 {
			rl.status = sr.status
		}
	})

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("[FATAL] Server crash: %v", err)
	}
}
