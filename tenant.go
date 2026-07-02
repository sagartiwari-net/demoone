package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
)

type tenantContextKey struct{}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func lookupWebsiteIDByHost(host string) int {
	host = normalizeHost(host)
	if host == "" || db == nil {
		return 0
	}
	var wid int
	err := db.QueryRow("SELECT id FROM ahrefs_websites WHERE LOWER(domain) = ?", host).Scan(&wid)
	if err != nil {
		return 0
	}
	return wid
}

func websiteIDForRequest(r *http.Request) int {
	if id := tenantWebsiteID(r.Context()); id > 0 {
		return id
	}
	if wid := lookupWebsiteIDByHost(r.Host); wid > 0 {
		return wid
	}
	cfg := loadConfig()
	if cfg.WebsiteID > 0 {
		return cfg.WebsiteID
	}
	return 1
}

func withTenantWebsiteID(ctx context.Context, websiteID int) context.Context {
	if websiteID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey{}, websiteID)
}

func tenantWebsiteID(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(tenantContextKey{}).(int); ok && v > 0 {
		return v
	}
	return 0
}

func validateRequestHostMatchesWebsite(r *http.Request, websiteID int) bool {
	if websiteID <= 0 {
		return true
	}
	hostID := lookupWebsiteIDByHost(r.Host)
	if hostID <= 0 {
		log.Printf("[SECURITY] Unknown host %q for website_id=%d", r.Host, websiteID)
		return false
	}
	return hostID == websiteID
}
