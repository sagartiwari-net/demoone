package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const extensionTemplateDir = "extension"

func publicOriginFromRequest(r *http.Request, cfg Config) (origin, host string) {
	host = strings.TrimSpace(cfg.PublicHost)
	if host == "" {
		host = r.Host
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			host = strings.Split(fwd, ",")[0]
			host = strings.TrimSpace(host)
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme := strings.TrimSpace(cfg.PublicScheme)
	if scheme == "" {
		scheme = "https"
		if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "http"
		}
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	origin = scheme + "://" + host
	return origin, host
}

func bakeExtensionBytes(origin, host string) ([]byte, error) {
	hostEsc := strings.ReplaceAll(host, ".", `\.`)
	replacer := strings.NewReplacer(
		"__H10_PROXY_ORIGIN__", origin,
		"__H10_PROXY_HOST__", host,
		"__H10_PROXY_HOST_ESC__", hostEsc,
	)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(extensionTemplateDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(extensionTemplateDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		ext := strings.ToLower(filepath.Ext(name))
		content := raw
		if ext == ".js" || ext == ".html" || ext == ".json" || ext == ".css" || ext == ".txt" {
			content = []byte(replacer.Replace(string(raw)))
		}
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		_, err = w.Write(content)
		return err
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func extensionInstallPageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Install Helium 10 Extension</title>
<link href="https://fonts.googleapis.com/css2?family=DM+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'DM Sans',system-ui,sans-serif;background:#eef1f6;color:#0f172a;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
.card{width:100%%;max-width:560px;background:#fff;border-radius:24px;box-shadow:0 18px 50px rgba(15,23,42,.08);padding:36px 32px}
.badge{display:inline-block;padding:6px 12px;border-radius:999px;background:#eef2ff;color:#4338ca;font-size:11px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;margin-bottom:14px}
h1{font-size:28px;font-weight:700;letter-spacing:-.02em;margin-bottom:10px}
.lead{color:#64748b;font-size:15px;line-height:1.6;margin-bottom:22px}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:8px;width:100%%;padding:14px 18px;border:0;border-radius:14px;background:#0f172a;color:#fff;font-size:15px;font-weight:600;text-decoration:none;cursor:pointer}
.btn:hover{background:#1e293b}
.steps{margin-top:28px;border-top:1px solid #eef2f7;padding-top:20px}
.steps h2{font-size:14px;font-weight:700;margin-bottom:12px;color:#0f172a}
.steps ol{padding-left:18px;color:#64748b;font-size:14px;line-height:1.7}
.steps li{margin-bottom:8px}
.steps code{background:#f1f5f9;padding:1px 6px;border-radius:6px;font-size:12px;color:#334155}
.note{margin-top:18px;font-size:12px;color:#94a3b8;line-height:1.5}
</style>
</head>
<body>
<div class="card">
  <div class="badge">Chrome Extension</div>
  <h1>Install Helium 10</h1>
  <p class="lead">Download the Chrome extension for Helium 10, then install it using the steps below.</p>
  <a class="btn" href="/extension.zip">Download Extension (.zip)</a>
  <div class="steps">
    <h2>Install steps</h2>
    <ol>
      <li>Download and unzip the file.</li>
      <li>Open <code>chrome://extensions</code> in Chrome.</li>
      <li>Enable <strong>Developer mode</strong> (top right).</li>
      <li>Click <strong>Load unpacked</strong> and select the unzipped folder.</li>
    </ol>
  </div>
  <p class="note">Chrome does not allow websites to install extensions by themselves. Use Developer mode and Load unpacked after downloading the zip.</p>
</div>
</body>
</html>`)
}

func extensionZipHandler(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	origin, host := publicOriginFromRequest(r, cfg)
	zipBytes, err := bakeExtensionBytes(origin, host)
	if err != nil {
		log.Printf("[EXT] ❌ bake failed host=%s: %v", host, err)
		http.Error(w, "Failed to build extension package", http.StatusInternalServerError)
		return
	}
	filename := "helium10-extension.zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(zipBytes)))
	w.Header().Set("Cache-Control", "no-store")
	log.Printf("[EXT] ✅ served extension.zip for origin=%s (%d bytes)", origin, len(zipBytes))
	_, _ = w.Write(zipBytes)
}
