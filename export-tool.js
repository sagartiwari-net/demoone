(function () {
  'use strict';
  let _xS = null;

  // Dynamic API Tracking
  window._lastTableApiRequest = null;

  function isRelevantTableApi(urlStr) {
      let lowerUrl = urlStr.toLowerCase();
      
      // Ignore irrelevant background calls
      const blacklistedKeywords = [
          '/notes', '/user', '/sentry', '/notification', '/profile', 
          '/features', '/billing', '/insights', '/announcements', 
          '/feedback', '/favorites', '/dpa/insights', '/limits', '/session',
          '/chart', '/summary', '/overview'
      ];
      for (let keyword of blacklistedKeywords) {
          if (lowerUrl.includes(keyword)) return false;
      }
      
      // JSON-RPC requests (like /dpa/rpc or /rpc) are the core table queries for positions/organic tools
      if (lowerUrl.includes('/rpc') || lowerUrl.includes('/dpa/rpc')) {
          return true;
      }
      
      let path = window.location.pathname.toLowerCase();
      
      // Match based on current Semrush tool context
      if (path.includes('/refdomains')) {
          return lowerUrl.includes('refdomains') || lowerUrl.includes('referring') || lowerUrl.includes('/api/');
      }
      if (path.includes('/backlinks')) {
          return lowerUrl.includes('backlinks') || lowerUrl.includes('backlink') || lowerUrl.includes('/api/');
      }
      if (path.includes('/organic/positions') || path.includes('/organic')) {
          return lowerUrl.includes('organic') || lowerUrl.includes('positions') || lowerUrl.includes('/api/') || lowerUrl.includes('/rpc');
      }
      if (path.includes('/adwords') || path.includes('/paid')) {
          return lowerUrl.includes('adwords') || lowerUrl.includes('/rpc') || lowerUrl.includes('/api/');
      }
      if (path.includes('/keyword')) {
          return lowerUrl.includes('keyword') || lowerUrl.includes('phrase') || lowerUrl.includes('kw') || lowerUrl.includes('/api/');
      }
      if (path.includes('/gap')) {
          return lowerUrl.includes('gap') || lowerUrl.includes('/api/');
      }
      
      return lowerUrl.includes('/api/') || lowerUrl.includes('/analytics/') || lowerUrl.includes('/rpc');
  }
  
  function _scoreTableApiUrl(urlStr) {
    if (!urlStr) return -1;
    const lower = String(urlStr).toLowerCase();
    let score = 0;
    if (lower.includes('action=report')) score += 100;
    if (lower.includes('display_page=')) score += 50;
    if (lower.includes('webapi2')) score += 40;
    if (lower.includes('offset=') || lower.includes('page=')) score += 20;
    if (lower.includes('/api/backlinks/list')) score += 5;
    if (lower.includes('/rpc')) score += 30;
    return score;
  }

  function _scoreTableApiRequest(urlStr, body) {
    let score = _scoreTableApiUrl(urlStr);
    const b = String(body || '').toLowerCase();
    if (!b) return score;
    if ((b.includes('adwords.positions') || b.includes('organic.positions') || b.includes('positions.get')) &&
        !b.includes('positionstotal')) score += 220;
    if (b.includes('positionstotal')) score += 30;
    if (b.includes('user.databases') || b.includes('snapshotdates') || b.includes('currency.rates')) score -= 300;
    if (b.includes('monthlyfulltrend') || b.includes('dailyfulltrend')) score -= 250;
    if (b.includes('"method":"token.get"') || b.includes('"method": "token.get"')) score -= 120;
    return score;
  }

  function _rememberTableApiRequest(urlStr, payload) {
    if (!isRelevantTableApi(urlStr, payload && payload.body)) return;
    const body = payload && (payload.body || (payload.init && payload.init.body));
    const apiKeyMatch = String(body || '').match(/"apiKey"\s*:\s*"([a-f0-9]{20,})"/i);
    if (apiKeyMatch) window._semrushCapturedApiKey = apiKeyMatch[1];
    const nextScore = _scoreTableApiRequest(urlStr, body);
    const prevScore = window._lastTableApiRequest
      ? _scoreTableApiRequest(window._lastTableApiRequest.url, window._lastTableApiRequest.body || (window._lastTableApiRequest.init && window._lastTableApiRequest.init.body))
      : -1;
    if (nextScore < prevScore) return;
    window._lastTableApiRequest = payload;
  }

  function _extractApiKey() {
    if (window._semrushCapturedApiKey) return window._semrushCapturedApiKey;
    if (window._lastTableApiRequest) {
      const sources = [
        window._lastTableApiRequest.body,
        window._lastTableApiRequest.init && window._lastTableApiRequest.init.body
      ];
      for (let i = 0; i < sources.length; i++) {
        const m = String(sources[i] || '').match(/"apiKey"\s*:\s*"([a-f0-9]{20,})"/i);
        if (m) return m[1];
      }
    }
    try {
      const entries = performance.getEntriesByType('resource');
      for (let i = entries.length - 1; i >= 0; i--) {
        const m = String(entries[i].name || '').match(/[?&]key=([a-f0-9]{20,})/i);
        if (m) return m[1];
      }
    } catch (e) {}
    const html = document.documentElement.innerHTML;
    const inline = html.match(/"apiKey"\s*:\s*"([a-f0-9]{20,})"/i) || html.match(/apiKey['"]\s*:\s*['"]([a-f0-9]{20,})/i);
    return inline ? inline[1] : null;
  }

  function _extractRpcPositionsCall(bodyStr) {
    if (!bodyStr) return null;
    try {
      let parsed = JSON.parse(String(bodyStr));
      const list = Array.isArray(parsed) ? parsed : [parsed];
      const item = list.find((entry) => _isPositionsRpcMethod(entry && entry.method));
      if (!item || !item.params) return null;
      const method = String(item.method || '');
      let reportType = 'adwords.positions';
      if (method.toLowerCase().includes('organic')) reportType = 'organic.positions';
      return {
        template: JSON.parse(JSON.stringify(item)),
        params: item.params,
        reportType: reportType
      };
    } catch (e) {
      return null;
    }
  }

  async function _buildRpcPageBody(capturedBody, pageNum, pageSize) {
    const ctx = _extractRpcPositionsCall(capturedBody);
    if (!ctx) return _prepareRpcPageBody(String(capturedBody), pageNum, pageSize);

    const p = ctx.params;
    const apiKey = p.apiKey || _extractApiKey();
    if (!apiKey) throw new Error('Could not find Semrush API key for export');

    const tokenReq = {
      id: 9000 + pageNum,
      jsonrpc: '2.0',
      method: 'token.Get',
      params: {
        reportType: ctx.reportType,
        database: p.database,
        date: p.date,
        dateType: p.dateType || 'daily',
        searchItem: p.searchItem,
        page: pageNum,
        pageSize: pageSize,
        userId: p.userId,
        apiKey: apiKey
      }
    };

    const tokenResp = await fetch(_absoluteProxyUrl('/dpa/rpc'), {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/plain, */*' },
      body: JSON.stringify(tokenReq)
    });
    if (!tokenResp.ok) throw new Error(`Token refresh failed (HTTP ${tokenResp.status})`);
    const tokenJson = await tokenResp.json();
    const token = tokenJson.result && tokenJson.result.token;
    if (!token) throw new Error((tokenJson.error && tokenJson.error.message) || 'Could not refresh export token');

    const posReq = JSON.parse(JSON.stringify(ctx.template));
    posReq.id = 7000 + pageNum;
    posReq.params.token = token;
    const walk = (obj) => {
      if (!obj || typeof obj !== 'object') return;
      if (Array.isArray(obj)) return obj.forEach(walk);
      if ('page' in obj) obj.page = pageNum;
      if ('pageIndex' in obj) obj.pageIndex = pageNum - 1;
      if ('pageSize' in obj) obj.pageSize = pageSize;
      Object.keys(obj).forEach((key) => walk(obj[key]));
    };
    walk(posReq);
    return JSON.stringify(posReq);
  }

  function _sanitizeFetchInit(init, method) {
    const clean = {};
    const upperMethod = String(method || init.method || 'GET').toUpperCase();
    clean.method = upperMethod;
    clean.mode = 'cors';
    clean.credentials = 'include';
    clean.cache = 'no-store';
    clean.redirect = 'follow';

    const blockedHeaderKeys = new Set([
      'host', 'connection', 'content-length', 'accept-encoding', 'origin', 'referer', 'cookie', 'sec-fetch-mode',
      'sec-fetch-site', 'sec-fetch-dest', 'sec-ch-ua', 'sec-ch-ua-mobile', 'sec-ch-ua-platform'
    ]);
    const headers = {};
    if (init && init.headers) {
      if (typeof Headers !== 'undefined' && init.headers instanceof Headers) {
        init.headers.forEach((value, key) => {
          if (!blockedHeaderKeys.has(String(key).toLowerCase())) headers[key] = value;
        });
      } else if (typeof init.headers === 'object') {
        Object.keys(init.headers).forEach((key) => {
          if (!blockedHeaderKeys.has(String(key).toLowerCase())) headers[key] = init.headers[key];
        });
      }
    }
    if (!headers.Accept) headers.Accept = 'application/json, text/plain, */*';
    if (upperMethod !== 'GET' && upperMethod !== 'HEAD' && init && init.body && !headers['Content-Type'] && !headers['content-type']) {
      headers['Content-Type'] = 'application/json';
    }
    clean.headers = headers;

    if (upperMethod !== 'GET' && upperMethod !== 'HEAD' && init && init.body) {
      clean.body = init.body;
    }
    return clean;
  }

  function _absoluteProxyUrl(url) {
    const proxied = proxyUrl(String(url || ''));
    if (!proxied) return proxied;
    if (/^https?:\/\//i.test(proxied)) return proxied;
    return new URL(proxied, window.location.origin).href;
  }

  function _isOfficialReportExportRequest(urlStr, bodyText) {
    if (!urlStr || urlStr.indexOf('/my_reports/') === -1) return false;
    const lowerUrl = urlStr.toLowerCase();
    const body = String(bodyText || '').toLowerCase();
    if (lowerUrl.includes('/graphql')) {
      return body.includes('createreport') || body.includes('generatereport') || body.includes('schedulereport') ||
        body.includes('liteorder') || body.includes('reportorder') || body.includes('downloadpdf');
    }
    if (lowerUrl.includes('/lite-order/') && body) return true;
    return false;
  }

  const originalFetch = window.fetch;
  window.fetch = function(input, init) {
      if (input) {
          let urlStr = '';
          if (typeof input === 'string') {
              urlStr = input;
          } else if (input instanceof Request) {
              urlStr = input.url;
          } else if (input instanceof URL) {
              urlStr = input.toString();
          }
          
          let capturedBody = init ? init.body : null;
          const method = (init && init.method) || (input instanceof Request ? input.method : 'GET');
          
          if (method.toUpperCase() === 'POST' && _isOfficialReportExportRequest(urlStr, capturedBody)) {
            if (!window._semrushPdfExportRunning) {
              window._semrushLastExportIntent = 'pdf';
              setTimeout(() => _exportPdf(), 0);
            }
            return Promise.resolve(new Response(JSON.stringify({
              data: { createReport: { id: 'blocked-by-proxy', status: 'ready', url: null } }
            }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
          }
          
          // Asynchronously read body if the request was made using a Request object
          if (!capturedBody && input instanceof Request) {
              try {
                  input.clone().text().then(text => {
                      if (isRelevantTableApi(urlStr, text)) {
                          if (window._lastTableApiRequest && window._lastTableApiRequest.url === urlStr) {
                              window._lastTableApiRequest.body = text;
                          }
                      } else {
                          // If it turned out to be an irrelevant chart/summary request, invalidate it
                          if (window._lastTableApiRequest && window._lastTableApiRequest.url === urlStr) {
                              window._lastTableApiRequest = null;
                          }
                      }
                  }).catch(() => {});
              } catch(e){}
          }
          
          if (isRelevantTableApi(urlStr, capturedBody)) {
              _rememberTableApiRequest(urlStr, {
                  url: urlStr,
                  init: init || {},
                  body: capturedBody
              });
          }
      }
      return originalFetch.apply(this, arguments);
  };

  const originalXHROpen = window.XMLHttpRequest.prototype.open;
  window.XMLHttpRequest.prototype.open = function(method, url, async, user, password) {
      this._semrushRpcUrl = url ? String(url) : '';
      this._semrushRpcMethod = method;
      this._semrushRpcHeaders = {};
      if (url) {
          let urlStr = String(url);
          if (isRelevantTableApi(urlStr, null)) {
              _rememberTableApiRequest(urlStr, {
                  url: urlStr,
                  method: method,
                  headers: {},
                  body: null
              });
          }
      }
      return originalXHROpen.apply(this, arguments);
  };

  const originalXHRSetHeader = window.XMLHttpRequest.prototype.setRequestHeader;
  window.XMLHttpRequest.prototype.setRequestHeader = function(header, value) {
      if (!this._semrushRpcHeaders) this._semrushRpcHeaders = {};
      this._semrushRpcHeaders[header] = value;
      if (window._lastTableApiRequest && window._lastTableApiRequest.headers) {
          window._lastTableApiRequest.headers[header] = value;
      }
      return originalXHRSetHeader.apply(this, arguments);
  };

  const originalXHRSend = window.XMLHttpRequest.prototype.send;
  window.XMLHttpRequest.prototype.send = function(body) {
      if (body && this._semrushRpcUrl && isRelevantTableApi(this._semrushRpcUrl, body)) {
          _rememberTableApiRequest(this._semrushRpcUrl, {
              url: this._semrushRpcUrl,
              method: this._semrushRpcMethod || 'POST',
              headers: Object.assign({}, this._semrushRpcHeaders || {}),
              body: body,
              init: {
                  method: this._semrushRpcMethod || 'POST',
                  body: body,
                  headers: Object.assign({}, this._semrushRpcHeaders || {})
              }
          });
      }
      return originalXHRSend.apply(this, arguments);
  };

  /* ================= EXPORT UTILITIES ================= */
  const _libCache = {};
  function _loadScript(src) {
    if (_libCache[src]) return _libCache[src];
    _libCache[src] = new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = src;
      s.async = true;
      s.onload = () => resolve();
      s.onerror = () => reject(new Error('Failed to load ' + src));
      document.head.appendChild(s);
    });
    return _libCache[src];
  }

  function _downloadBlob(blob, filename) {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(a.href);
  }

  function _slugName() {
    const path = window.location.pathname.replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '');
    const q = new URLSearchParams(window.location.search).get('q') || '';
    const base = (q || path || 'semrush').replace(/[^\w.-]+/g, '-');
    return base.slice(0, 80) || 'semrush-export';
  }

  function _isPdfBtn(el) {
    if (!el) return false;
    const text = (el.textContent || '').toLowerCase();
    const label = (el.getAttribute('aria-label') || '').toLowerCase();
    return text.includes('pdf') || label.includes('pdf');
  }

  function _hideOfficialModals() {
    const needles = [
      'guru plan', 'export is limited', 'upgrade to guru', 'switch to the guru',
      'export settings', 'send to my email', 'share via email',
      'create online dashboard', 'embed via iframe', 'no semrush mentions',
      'notify you by email', 'view report balance', 'schedule report',
      'generate the report', 'few more minutes to generate'
    ];
    let blockedExportSettings = false;
    document.querySelectorAll('[role="dialog"], [aria-modal="true"], [class*="modal" i], [class*="popup" i], [class*="drawer" i], [data-testid*="modal" i], [class*="overlay" i]').forEach((el) => {
      if (el.closest('#semrush-export-container, #semrush-pdf-progress')) return;
      const t = (el.textContent || '').toLowerCase();
      if (needles.some((n) => t.includes(n))) {
        el.style.setProperty('display', 'none', 'important');
        el.style.setProperty('visibility', 'hidden', 'important');
        el.style.setProperty('pointer-events', 'none', 'important');
        el.setAttribute('data-semrush-export-hidden', '1');
        if (t.includes('export settings') || t.includes('download pdf')) blockedExportSettings = true;
      }
    });
    document.querySelectorAll('body > div').forEach((el) => {
      if (el.id === 'semrush-export-container' || el.id === 'semrush-pdf-progress') return;
      const t = (el.textContent || '').toLowerCase();
      if (t.includes('export settings') && t.includes('download pdf')) {
        el.style.setProperty('display', 'none', 'important');
        el.style.setProperty('visibility', 'hidden', 'important');
        el.style.setProperty('pointer-events', 'none', 'important');
        blockedExportSettings = true;
      }
    });
    if (blockedExportSettings && !window._semrushPdfExportRunning) {
      const picker = document.getElementById('semrush-export-container');
      const pickerOpen = picker && picker.style.display === 'block';
      if (!pickerOpen && !window._semrushExportFallbackRan) {
        window._semrushExportFallbackRan = true;
        setTimeout(() => {
          window._semrushExportFallbackRan = false;
          if (window._semrushLastExportIntent === 'table') {
            _uiFormatPicker();
          } else {
            _exportPdf();
          }
        }, 100);
      }
    }
  }

  function _hideUpgradeModal() {
    _hideOfficialModals();
  }

  (function _startUpgradeBlocker() {
    _hideOfficialModals();
    const obs = new MutationObserver(_hideOfficialModals);
    obs.observe(document.documentElement, { childList: true, subtree: true });
    setInterval(_hideOfficialModals, 400);
  })();

  function _findExportButton(target) {
    if (!target || !target.closest) return null;
    const selectors = [
      '[data-testid="export-button"]',
      '[data-test-id="export-button"]',
      '[data-ui-name="ExportButton"]',
      '[data-test-export-btn]',
      'button[aria-label*="export" i]',
      'a[aria-label*="export" i]',
      '.sm-export-button-trigger',
      '[data-at*="export" i]',
      '[class*="export-button" i]'
    ];
    for (let i = 0; i < selectors.length; i++) {
      const el = target.closest(selectors[i]);
      if (el) return el;
    }
    let el = target;
    for (let depth = 0; depth < 12 && el; depth++) {
      if (!el.matches) { el = el.parentElement; continue; }
      if (el.matches('button, [role="button"], [role="menuitem"], a, li, [class*="card" i], [class*="tile" i], div[tabindex], [data-testid*="export" i]')) {
        const text = (el.textContent || '').trim().toLowerCase();
        const label = (el.getAttribute('aria-label') || '').toLowerCase();
        const testId = (el.getAttribute('data-testid') || '').toLowerCase();
        if (
          text === 'export' || text === 'export to pdf' || /^export\b/.test(text) ||
          text.includes('download pdf') || label.includes('export') || label.includes('pdf') ||
          testId.includes('export')
        ) {
          return el;
        }
      }
      el = el.parentElement;
    }
    return null;
  }

  function _scrapeMetricBlocks() {
    const rows = [];
    const seen = new Set();
    const add = (label, value) => {
      const k = label + '|' + value;
      if (!label || !value || seen.has(k)) return;
      seen.add(k);
      rows.push([label.replace(/\s+/g, ' ').trim(), value.replace(/\s+/g, ' ').trim()]);
    };

    document.querySelectorAll('[data-testid*="metric"], [data-at*="metric"], [class*="summary-card"], [class*="SummaryCard"], [class*="kpi"], [class*="Kpi"]').forEach(card => {
      const txt = (card.innerText || '').trim();
      if (!txt || txt.length > 400) return;
      const lines = txt.split('\n').map(l => l.trim()).filter(Boolean);
      if (lines.length >= 2) add(lines[0], lines.slice(1).join(' '));
    });

    document.querySelectorAll('dl').forEach(dl => {
      dl.querySelectorAll('dt').forEach(dt => {
        const dd = dt.nextElementSibling;
        if (dd) add(dt.textContent, dd.textContent);
      });
    });

    return rows;
  }

  function _collectExportTableData() {
    if (_isSiteAuditIssuesList()) {
      const { headers, rows } = _expSiteAuditIssues();
      return { headers, rows };
    }
    const t = _tbl();
    if (!t) return null;
    const { topH, headers, indices } = _getValidColumns(t);
    const finalHeaders = indices.map(i => headers[i]);
    const rows = _rows(t, topH, indices);
    if (!rows.length) return null;
    return { headers: finalHeaders, rows };
  }

  async function _exportAsCsv(headers, rows, filename) {
    const content = _csv(headers, rows);
    const bom = '\uFEFF';
    _downloadBlob(new Blob([bom + content], { type: 'text/csv;charset=utf-8;' }), filename);
  }

  async function _exportAsXlsx(headers, rows, filename) {
    try {
      await _loadScript('https://cdn.sheetjs.com/xlsx-0.20.3/package/dist/xlsx.full.min.js');
      const ws = XLSX.utils.aoa_to_sheet([headers, ...rows]);
      const wb = XLSX.utils.book_new();
      XLSX.utils.book_append_sheet(wb, ws, 'Export');
      XLSX.writeFile(wb, filename);
      return;
    } catch (e) {
      console.warn('[Export] XLSX lib failed, using Excel XML fallback', e);
    }
    const esc = v => String(v == null ? '' : v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    let xml = '<?xml version="1.0"?><?mso-application progid="Excel.Sheet"?><Workbook xmlns="urn:schemas-microsoft-com:office:spreadsheet" xmlns:ss="urn:schemas-microsoft-com:office:spreadsheet"><Worksheet ss:Name="Export"><Table>';
    const all = [headers, ...rows];
    all.forEach(r => {
      xml += '<Row>';
      r.forEach(c => { xml += '<Cell><Data ss:Type="String">' + esc(c) + '</Data></Cell>'; });
      xml += '</Row>';
    });
    xml += '</Table></Worksheet></Workbook>';
    _downloadBlob(new Blob([xml], { type: 'application/vnd.ms-excel' }), filename.replace(/\.xlsx$/i, '.xls'));
  }

  async function _exportData(format) {
    const ext = format === 'xlsx' ? 'xlsx' : 'csv';
    const name = _slugName() + '-export.' + ext;
    const table = _collectExportTableData();
    if (table) {
      if (format === 'xlsx') await _exportAsXlsx(table.headers, table.rows, name);
      else await _exportAsCsv(table.headers, table.rows, name);
      return true;
    }
    return false;
  }

  function _waitFor(predicate, timeoutMs, intervalMs) {
    timeoutMs = timeoutMs || 30000;
    intervalMs = intervalMs || 250;
    return new Promise((resolve, reject) => {
      const start = Date.now();
      const tick = () => {
        try {
          if (predicate()) return resolve(true);
        } catch (e) {}
        if (Date.now() - start >= timeoutMs) return reject(new Error('Timed out waiting for PDF export'));
        setTimeout(tick, intervalMs);
      };
      tick();
    });
  }

  function _showPdfProgress(msg) {
    let el = document.getElementById('semrush-pdf-progress');
    if (!el) {
      el = document.createElement('div');
      el.id = 'semrush-pdf-progress';
      el.style.cssText = 'position:fixed;inset:0;z-index:10000000;background:rgba(0,0,0,.45);display:flex;align-items:center;justify-content:center;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;';
      el.innerHTML = '<div style="background:#fff;padding:28px 36px;border-radius:12px;max-width:420px;text-align:center;box-shadow:0 8px 32px rgba(0,0,0,.2)"><div style="width:42px;height:42px;border:4px solid #e5e5e5;border-top-color:#4c00c7;border-radius:50%;margin:0 auto 16px;animation:semrushSpin 1s linear infinite"></div><div id="semrush-pdf-progress-text" style="font-size:15px;font-weight:600;color:#111"></div><div style="font-size:12px;color:#666;margin-top:8px">Building PDF from page data</div></div><style>@keyframes semrushSpin{to{transform:rotate(360deg)}}</style>';
      document.body.appendChild(el);
    }
    const t = document.getElementById('semrush-pdf-progress-text');
    if (t) t.textContent = msg || 'Generating PDF...';
    el.style.display = 'flex';
  }

  function _hidePdfProgress() {
    const el = document.getElementById('semrush-pdf-progress');
    if (el) el.style.display = 'none';
  }

  function _reportMeta() {
    const sp = new URLSearchParams(window.location.search);
    const domain = _normalizeDomain(sp.get('q') || sp.get('domain') || document.title.split(':').pop() || document.title);
    const db = (sp.get('db') || 'us').toUpperCase();
    const device = (sp.get('device') || 'desktop').toLowerCase().indexOf('mob') !== -1 ? 'Mobile' : 'Desktop';
    const now = new Date();
    const dateStr = now.toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric' });
    const scope = sp.get('searchType') || 'domain';
    const path = window.location.pathname.toLowerCase();
    const pageTitle = _getPageReportName(path, document.title);
    return { domain, db, device, dateStr, scope, path, pageTitle, url: window.location.href };
  }

  function _getPageReportName(path, docTitle) {
    const map = [
      ['/analytics/overview', 'Domain Overview'],
      ['/analytics/backlinks', 'Backlinks'],
      ['/analytics/refdomains', 'Referring Domains'],
      ['/analytics/organic', 'Organic Research'],
      ['/analytics/keyword', 'Keyword Overview'],
      ['/analytics/traffic', 'Traffic Analytics'],
      ['/siteaudit', 'Site Audit'],
      ['/position-tracking', 'Position Tracking'],
      ['/keywordmagic', 'Keyword Magic Tool'],
      ['/keywordoverview', 'Keyword Overview']
    ];
    for (const [needle, name] of map) {
      if (path.includes(needle)) return name;
    }
    const h1 = document.querySelector('main h1, main h2, [data-testid*="title"]');
    if (h1 && (h1.textContent || '').trim()) {
      return (h1.textContent || '').trim().split(':')[0].trim().slice(0, 80);
    }
    if (docTitle && docTitle.includes(':')) {
      return docTitle.split(':')[0].trim().slice(0, 80);
    }
    const part = path.split('/').filter(Boolean).pop();
    return part ? part.replace(/[-_]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase()) : 'Report';
  }

  function _pdfSlug(meta) {
    return (meta.domain + '-' + meta.pageTitle).replace(/[^\w.-]+/g, '-').replace(/-+/g, '-').slice(0, 100);
  }

  function _findTableTitle(table) {
    let el = table;
    for (let i = 0; i < 10 && el; i++) {
      const h = el.querySelector(':scope > h1, :scope > h2, :scope > h3, :scope > h4, [class*="title" i], [class*="Title"]');
      if (h && (h.textContent || '').trim()) return (h.textContent || '').trim().slice(0, 120);
      el = el.parentElement;
    }
    return 'Data';
  }

  function _cleanCellValue(v) {
    let s = String(v == null ? '' : v).trim();
    const hyper = s.match(/^=HYPERLINK\(["']([^"']+)["'],\s*["']([^"']*)["']\)$/i);
    if (hyper) {
      const url = hyper[1];
      const label = hyper[2];
      return label ? label + '\n' + url : url;
    }
    return s.replace(/\s+/g, ' ').slice(0, 500);
  }

  function _scrapePageMetrics() {
    const out = [];
    const seen = new Set();
    const add = (label, value) => {
      label = _cleanCellValue(label);
      value = _cleanCellValue(value);
      if (!label || !value || label.length > 80 || value.length > 80) return;
      const k = label.toLowerCase();
      if (seen.has(k)) return;
      seen.add(k);
      out.push([label, value]);
    };

    const numLike = (s) => /^[~≈]?[\d,.]+[KMBkmb%]?$/.test(String(s).replace(/\s/g, ''));

    document.querySelectorAll('main [role="tab"], main [role="tablist"] > *, main [class*="summary" i], main [class*="kpi" i], main [class*="metric" i], main [class*="tab" i] button, main [class*="Tab" i]').forEach((el) => {
      if (el.closest('#semrush-export-container, #semrush-pdf-progress, [role="dialog"], table, [role="grid"]')) return;
      const lines = (el.innerText || '').split('\n').map((l) => l.trim()).filter(Boolean);
      if (lines.length < 2 || lines.length > 8) return;
      if (el.getBoundingClientRect().height > 260) return;
      if (numLike(lines[1]) || numLike(lines[lines.length - 1])) {
        add(lines[0], lines.slice(1).join(' — '));
      }
    });

    document.querySelectorAll('main button, main article, main section > div, main [class*="card" i]').forEach((el) => {
      if (el.closest('#semrush-export-container, #semrush-pdf-progress, [role="dialog"], table, [role="grid"]')) return;
      const lines = (el.innerText || '').split('\n').map((l) => l.trim()).filter(Boolean);
      if (lines.length === 2 && lines[0].length < 50 && lines[1].length < 40) {
        add(lines[0], lines[1]);
      }
    });

    _scrapeMetricBlocks().forEach(([l, v]) => add(l, v));
    return out;
  }

  function _scrapeMainTable(maxRows) {
    const t = _tbl();
    if (!t) return null;
    if (t.closest('#semrush-export-container, #semrush-pdf-progress')) return null;
    const { topH, headers, indices } = _getValidColumns(t);
    if (!indices.length) return null;
    const finalHeaders = indices.map((i) => _cleanCellValue(headers[i]));
    const rows = _rows(t, topH, indices).slice(0, maxRows || 300).map((row) => row.map(_cleanCellValue));
    if (!rows.length) return null;
    let title = '';
    const h = document.querySelector('main h1, main h2, [class*="header" i] h2, [data-testid*="title"]');
    if (h) title = (h.textContent || '').trim();
    if (!title) title = _findTableTitle(t);
    return { title: title.slice(0, 120) || 'Data', headers: finalHeaders, rows };
  }

  function _collectPdfTables() {
    const tables = [];
    const seen = new Set();
    const main = _scrapeMainTable(500);
    if (main) {
      seen.add(main.headers.join('|'));
      tables.push(main);
    }
    document.querySelectorAll('table, [role="table"], [role="grid"]').forEach((t) => {
      if (t.closest('#semrush-export-container, #semrush-pdf-progress, [role="dialog"]')) return;
      const { topH, headers, indices } = _getValidColumns(t);
      if (!indices.length) return;
      const finalHeaders = indices.map((i) => _cleanCellValue(headers[i]));
      const key = finalHeaders.join('|');
      if (seen.has(key)) return;
      const rows = _rows(t, topH, indices).slice(0, 100).map((row) => row.map(_cleanCellValue));
      if (!rows.length) return;
      seen.add(key);
      tables.push({ title: _findTableTitle(t), headers: finalHeaders, rows });
    });
    return tables;
  }

  function _pdfFooter(pdf, meta, pageIndex, pageCount) {
    const ph = pdf.internal.pageSize.getHeight();
    pdf.setFontSize(8);
    pdf.setTextColor(120, 120, 120);
    pdf.text('Generated on ' + meta.dateStr + '  |  ' + meta.pageTitle + ' Report  |  Page ' + pageIndex + ' of ' + pageCount, 14, ph - 8);
  }

  function _buildLocalPdfDocument(pdf, meta, metrics, tables) {
    const pw = pdf.internal.pageSize.getWidth();

    pdf.setFillColor(76, 0, 199);
    pdf.rect(0, 0, pw, 58, 'F');
    pdf.setTextColor(255, 255, 255);
    pdf.setFont('helvetica', 'normal');
    pdf.setFontSize(10);
    pdf.text(meta.pageTitle + ' (' + meta.device + ')', 14, 16);
    pdf.setFont('helvetica', 'bold');
    pdf.setFontSize(22);
    const domainLines = pdf.splitTextToSize(meta.domain, pw - 28);
    pdf.text(domainLines, 14, 30);
    pdf.setFont('helvetica', 'normal');
    pdf.setFontSize(10);
    pdf.text(meta.db + ' | ' + meta.scope + ' | ' + meta.dateStr, 14, 30 + domainLines.length * 7 + 4);

    let startY = 68 + domainLines.length * 4;

    if (metrics.length) {
      pdf.setTextColor(20, 20, 20);
      pdf.setFont('helvetica', 'bold');
      pdf.setFontSize(14);
      pdf.text('Summary', 14, startY);
      pdf.autoTable({
        startY: startY + 6,
        head: [['Metric', 'Value']],
        body: metrics.slice(0, 30),
        theme: 'grid',
        headStyles: { fillColor: [76, 0, 199], textColor: 255, fontStyle: 'bold' },
        styles: { fontSize: 9, cellPadding: 3, overflow: 'linebreak' },
        columnStyles: { 0: { cellWidth: 70 }, 1: { cellWidth: 'auto' } },
        margin: { left: 14, right: 14 }
      });
      startY = pdf.lastAutoTable.finalY + 10;
    }

    tables.forEach((tbl, idx) => {
      if (startY > 250) {
        pdf.addPage();
        startY = 20;
      }
      pdf.setFont('helvetica', 'bold');
      pdf.setFontSize(11);
      pdf.setTextColor(20, 20, 20);
      const titleLines = pdf.splitTextToSize(tbl.title, pw - 28);
      pdf.text(titleLines, 14, startY);
      pdf.setFont('helvetica', 'normal');
      pdf.setFontSize(9);
      pdf.setTextColor(100, 100, 100);
      pdf.text(meta.db + ' | ' + meta.domain + ' | ' + tbl.rows.length + ' rows', 14, startY + titleLines.length * 5 + 2);

      pdf.autoTable({
        startY: startY + titleLines.length * 5 + 8,
        head: [tbl.headers],
        body: tbl.rows,
        theme: 'striped',
        headStyles: { fillColor: [76, 0, 199], textColor: 255, fontStyle: 'bold', fontSize: 7 },
        styles: { fontSize: 7, cellPadding: 2, overflow: 'linebreak', cellWidth: 'wrap' },
        margin: { left: 8, right: 8 },
        showHead: 'everyPage'
      });
      startY = pdf.lastAutoTable.finalY + 12;
    });

    const pageCount = pdf.internal.getNumberOfPages();
    for (let i = 1; i <= pageCount; i++) {
      pdf.setPage(i);
      _pdfFooter(pdf, meta, i, pageCount);
    }
  }

  async function _exportLocalPdf() {
    if (window._semrushPdfExportRunning) return;
    window._semrushPdfExportRunning = true;
    try {
      _showPdfProgress('Collecting page data...');
      const meta = _reportMeta();
      const metrics = _scrapePageMetrics();
      const tables = _collectPdfTables();

      if (!metrics.length && !tables.length) {
        throw new Error('No report data found. Wait for the page to fully load, then try again.');
      }

      _showPdfProgress('Building PDF...');
      await _loadScript('https://cdnjs.cloudflare.com/ajax/libs/jspdf/2.5.1/jspdf.umd.min.js');
      await _loadScript('https://cdnjs.cloudflare.com/ajax/libs/jspdf-autotable/3.8.2/jspdf.plugin.autotable.min.js');

      const { jsPDF } = window.jspdf;
      const pdf = new jsPDF({ orientation: 'l', unit: 'mm', format: 'a4' });
      _buildLocalPdfDocument(pdf, meta, metrics, tables);

      const filename = _pdfSlug(meta) + '-report.pdf';
      pdf.save(filename);
      _hidePdfProgress();
    } catch (err) {
      console.error('[Local PDF export]', err);
      _hidePdfProgress();
      alert('PDF export failed: ' + (err.message || err));
    } finally {
      window._semrushPdfExportRunning = false;
    }
  }

  async function _downloadFromUrl(url, filename) {
    const resp = await fetch(proxyUrl(url), { credentials: 'include' });
    if (!resp.ok) throw new Error('PDF download failed (HTTP ' + resp.status + ')');
    const blob = await resp.blob();
    _downloadBlob(blob, filename);
  }

  function _normalizeDomain(raw) {
    if (!raw) return '';
    let d = String(raw).trim();
    if (d.startsWith('http://') || d.startsWith('https://')) {
      try { d = new URL(d).hostname; } catch (e) { d = d.replace(/^https?:\/\//, '').split('/')[0]; }
    }
    return d.replace(/^www\./, '').split('/')[0];
  }

  function _buildOfficialTemplateParams() {
    const sp = new URLSearchParams(window.location.search);
    const path = window.location.pathname.toLowerCase();
    const scopeMap = { domain: 'root_domain', subdomain: 'domain', subfolder: 'root_domain', url: 'url' };
    const searchType = sp.get('searchType') || 'domain';
    const domain = _normalizeDomain(sp.get('q') || sp.get('domain') || document.title.split(':')[0]);
    const dbCode = (sp.get('db') || 'us').toLowerCase();
    let device = (sp.get('device') || 'desktop').toLowerCase();
    const deviceType = device.indexOf('mob') !== -1 ? 'mobile' : 'desktop';

    if (path.indexOf('/analytics/overview') !== -1 || path.indexOf('/domain/overview') !== -1) {
      const deviceLabel = deviceType === 'mobile' ? 'Mobile' : 'Desktop';
      return {
        period: sp.get('period') || '1y',
        device_type: deviceLabel,
        domain: domain,
        currency: (sp.get('currency') || 'usd').toLowerCase(),
        db: dbCode,
        db_date: 'current',
        report_scope: scopeMap[searchType] || 'root_domain',
        scope: searchType,
        type: 'domain_overview',
        order: 'traffic_desc',
        display_sort: 'tr_desc'
      };
    }

    throw new Error('Official PDF export is supported on Domain Overview pages (/analytics/overview/)');
  }

  async function _ensureLiteOrderAssets() {
    if (window.liteOrder) return;
    const resp = await fetch(proxyUrl('/my_reports/api/v1/lite-order'), { credentials: 'include' });
    const data = await resp.json();
    if (!data || data.status !== 'success' || !data.result) {
      throw new Error('Could not load Semrush PDF exporter');
    }
    const scriptUrl = proxyUrl(data.result.script);
    const styleUrl = proxyUrl(data.result.styles);
    if (!document.querySelector('link[data-semrush-lo-css]')) {
      await new Promise((resolve, reject) => {
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = styleUrl;
        link.setAttribute('data-semrush-lo-css', '1');
        link.onload = () => resolve();
        link.onerror = () => reject(new Error('Failed to load PDF styles'));
        document.head.appendChild(link);
      });
    }
    await new Promise((resolve, reject) => {
      const existing = document.querySelector('script[data-semrush-lo-js]');
      if (existing && window.liteOrder) return resolve();
      const s = document.createElement('script');
      s.src = scriptUrl;
      s.async = true;
      s.setAttribute('data-semrush-lo-js', '1');
      s.onload = () => setTimeout(resolve, 400);
      s.onerror = () => reject(new Error('Failed to load PDF script'));
      document.body.appendChild(s);
    });
    await _waitFor(() => !!window.liteOrder, 30000);
  }

  function _findLiteOrderModalExportBtn() {
    const roots = [...document.querySelectorAll('[class*="modal___"], [role="dialog"]')]
      .filter((el) => !el.closest('#semrush-export-container') && !el.closest('#semrush-pdf-progress'));
    for (const root of roots) {
      const btn = [...root.querySelectorAll('button, [role="button"]')].find((b) => /^export to pdf$/i.test((b.textContent || '').trim()));
      if (btn) return btn;
    }
    return null;
  }

  function _ensureLiteOrderHideStyle() {
    if (document.getElementById('semrush-lo-hide-style')) return;
    const st = document.createElement('style');
    st.id = 'semrush-lo-hide-style';
    st.textContent = '[class*="modal___qxsDQ"]{position:fixed!important;left:-10000px!important;top:0!important;width:720px!important;opacity:0.01!important;z-index:2147483640!important;}';
    document.head.appendChild(st);
  }

  const HIJACK_EVENTS = ['click', 'mousedown', 'pointerdown'];

  function _waitLiteOrderInit(timeoutMs) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        window.removeEventListener('lite-order:init', onInit);
        reject(new Error('lite-order init timeout'));
      }, timeoutMs);
      function onInit(e) {
        clearTimeout(timer);
        window.removeEventListener('lite-order:init', onInit);
        resolve(e.detail || {});
      }
      window.addEventListener('lite-order:init', onInit);
    });
  }

  function _triggerNativePdfExportClick() {
    const pdfBtn = [...document.querySelectorAll('button, [role="menuitem"], a, [role="button"]')].find((b) => {
      const text = (b.textContent || '').trim();
      return /^export to pdf$/i.test(text);
    });
    if (!pdfBtn) throw new Error('Native Export to PDF button not found');
    pdfBtn.click();
  }

  function _isGuruUpgradeBlocked() {
    return !!document.querySelector('[data-semrush-export-hidden="1"]');
  }

  function _buildOfficialExportPayload(templateParameters) {
    return {
      templateParameters: templateParameters,
      emailing: {
        isEnabled: false,
        emails: [],
        subject: '',
        message: '',
        senderEmailName: '',
        emailToReplyTo: '',
        signText: '',
        senderName: '',
        signImageId: 0
      },
      scheduling: {
        isEnabled: false,
        period: 'monthly',
        value: 28,
        generationStartHour: null
      },
      branding: {
        isEnabled: false,
        logoId: 1,
        logoUrl: '',
        text: ''
      },
      isTOCEnabled: false,
      ignoreRestrictions: true,
      publicDashboard: null,
      state: 'premium'
    };
  }

  async function _postOfficialPdfExport(templateType, templateParameters) {
    const resp = await fetch(proxyUrl('/my_reports/api/v1/lite-order/' + templateType), {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        'Accept': 'application/json',
        'X-Requested-With': 'XMLHttpRequest'
      },
      body: JSON.stringify(_buildOfficialExportPayload(templateParameters))
    });
    let data = {};
    try { data = await resp.json(); } catch (e) {}
    if (data && data.status === 'success') {
      const url = data.url || data.result?.url || data.task?.url || data.result?.task?.url;
      if (url) return url;
    }
    if (resp.status === 202 && data && data.type === 'reportGenerationTimeout') {
      throw new Error('reportGenerationTimeout');
    }
    throw new Error((data && data.message) || 'Official PDF API failed (HTTP ' + resp.status + ')');
  }

  async function _exportOfficialPdfViaApi(templateType, templateParameters) {
    const url = await _postOfficialPdfExport(templateType, templateParameters);
    const name = _slugName() + '-Domain-Overview-report.pdf';
    await _downloadFromUrl(url, name);
  }

  async function _exportOfficialPdfViaNativeModal() {
    _ensureLiteOrderHideStyle();
    const initPromise = _waitLiteOrderInit(25000).catch(() => null);
    _triggerNativePdfExportClick();
    const initDetail = await initPromise;
    await _ensureLiteOrderAssets();

    if (initDetail && initDetail.templateParameters && window.liteOrder) {
      window.liteOrder.init({
        templateType: initDetail.templateType || initDetail.templateParameters.type || 'domain_overview',
        templateParameters: initDetail.templateParameters,
        enabledButtons: { export: true, custom: false },
        enabledCheckboxes: { emailing: false, tableOfContent: false },
        onReportRequestDataHandler: function () {
          return _buildOfficialExportPayload(initDetail.templateParameters);
        }
      });
      window.liteOrder.setVisibility(true);
    }

    await _waitFor(() => !!_findLiteOrderModalExportBtn(), 90000);
    const exportBtn = _findLiteOrderModalExportBtn();
    if (!exportBtn) throw new Error('Could not find Export to PDF in report dialog');
    exportBtn.click();
    await _waitFor(() => !!window._semrushCapturedPdfUrl, 180000);
    if (!window._semrushCapturedPdfUrl) throw new Error('PDF URL was not returned');
    const name = _slugName() + '-Domain-Overview-report.pdf';
    await _downloadFromUrl(window._semrushCapturedPdfUrl, name);
    window._semrushCapturedPdfUrl = null;
  }

  async function _exportOfficialPdfViaUi(templateType, templateParameters) {
    if (!document.getElementById('my-reports-lite-order')) {
      const mount = document.createElement('div');
      mount.id = 'my-reports-lite-order';
      document.body.appendChild(mount);
    }
    if (!document.getElementById('semrush-lo-hide-style')) {
      _ensureLiteOrderHideStyle();
    }

    window.liteOrder.init({
      templateType: templateType,
      templateParameters: templateParameters,
      enabledButtons: { export: true, custom: false },
      enabledCheckboxes: { emailing: false, tableOfContent: false },
      onReportRequestDataHandler: function (data) {
        return _buildOfficialExportPayload(templateParameters);
      }
    });

    window.liteOrder.setVisibility(true);

    await _waitFor(() => !!_findLiteOrderModalExportBtn(), 120000);
    const exportBtn = _findLiteOrderModalExportBtn();
    if (!exportBtn) throw new Error('Could not find Export to PDF in report dialog');
    exportBtn.click();
    await _waitFor(() => !!window._semrushCapturedPdfUrl, 180000);
    if (!window._semrushCapturedPdfUrl) throw new Error('PDF URL was not returned');
    const name = _slugName() + '-Domain-Overview-report.pdf';
    await _downloadFromUrl(window._semrushCapturedPdfUrl, name);
    window._semrushCapturedPdfUrl = null;
  }

  async function _exportOfficialPdf() {
    if (window._semrushPdfExportRunning) return;
    window._semrushPdfExportRunning = true;
    const origOpen = window.open;
    window._semrushCapturedPdfUrl = null;

    window.open = function (url, target, features) {
      if (url && typeof url === 'string' && (url.indexOf('.pdf') !== -1 || url.indexOf('/my_reports/') !== -1)) {
        window._semrushCapturedPdfUrl = proxyUrl(url);
        const name = _slugName() + '-Domain-Overview-report.pdf';
        _showPdfProgress('Downloading PDF...');
        _downloadFromUrl(window._semrushCapturedPdfUrl, name).then(() => {
          _hidePdfProgress();
        }).catch((err) => {
          _hidePdfProgress();
          alert('PDF download failed: ' + err.message);
        });
        return null;
      }
      return origOpen.apply(this, arguments);
    };

    try {
      _showPdfProgress('Loading Semrush report engine...');
      const params = _buildOfficialTemplateParams();
      const templateType = params.type;

      try {
        _showPdfProgress('Opening official report exporter...');
        await _exportOfficialPdfViaNativeModal();
        _hidePdfProgress();
        return;
      } catch (nativeErr) {
        console.warn('[Official PDF] Native modal path failed', nativeErr);
      }

      try {
        _showPdfProgress('Generating official PDF report...');
        await _exportOfficialPdfViaApi(templateType, params);
        _hidePdfProgress();
        return;
      } catch (apiErr) {
        console.warn('[Official PDF] API path failed, trying UI exporter', apiErr);
      }

      await _ensureLiteOrderAssets();
      _showPdfProgress('Generating official PDF report...');
      await _exportOfficialPdfViaUi(templateType, params);
      _hidePdfProgress();
    } catch (err) {
      console.error('[Official PDF export]', err);
      _hidePdfProgress();
      let msg = err.message || String(err);
      if (_isGuruUpgradeBlocked()) {
        msg = 'PDF export needs a Guru/paid Semrush session in cookie.txt. Current session shows the Guru upgrade wall.';
      }
      alert('Official PDF export failed: ' + msg + '\n\nOpen Domain Overview, then click Export to PDF again.');
    } finally {
      window.open = origOpen;
      window._semrushPdfExportRunning = false;
      window._semrushCapturedPdfUrl = null;
      try {
        if (window.liteOrder) window.liteOrder.setVisibility(false);
      } catch (e) {}
    }
  }

  async function _exportPdf() {
    return _exportLocalPdf();
  }

  function _showExportPanel(c, name) {
    ['format', 'scope', 'multi'].forEach((p) => {
      const el = c.querySelector('[data-export-panel="' + p + '"]');
      if (el) el.style.display = p === name ? 'block' : 'none';
    });
  }

  function _uiFormatPicker(anchorEl) {
    let c = document.getElementById('semrush-export-container');
    if (c) {
      if (anchorEl && anchorEl.getBoundingClientRect) {
        const r = anchorEl.getBoundingClientRect();
        c.style.top = Math.min(r.bottom + 8, window.innerHeight - 320) + 'px';
        c.style.left = Math.min(Math.max(8, r.right - 300), window.innerWidth - 300) + 'px';
        c.style.transform = 'none';
      }
      const scopeTitle = document.getElementById('semrush-export-scope-title');
      if (scopeTitle && window._exportFormat) {
        scopeTitle.textContent = (window._exportFormat === 'xlsx' ? 'Excel' : 'CSV') + ' — choose scope';
      }
      _showExportPanel(c, 'format');
      window._exportFormat = null;
      const st = document.getElementById('semrush-export-status');
      if (st) st.textContent = '';
      c.style.display = 'block';
      return;
    }
    c = document.createElement('div');
      c.id = 'semrush-export-container';
      c.style.cssText = 'position:fixed;z-index:9999999;background:#fff;padding:16px 18px;border-radius:10px;box-shadow:0 8px 32px rgba(0,0,0,.2);font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;min-width:280px;color:#111;border:1px solid #e0e0e0;';
      document.body.appendChild(c);

      const closeBtn = document.createElement('div');
      closeBtn.innerHTML = '&times;';
      closeBtn.style.cssText = 'cursor:pointer;font-size:20px;color:#888;text-align:right;margin:-2px 0 6px;line-height:1;';
      closeBtn.onclick = () => { c.style.display = 'none'; };

      const status = document.createElement('div');
      status.id = 'semrush-export-status';
      status.style.cssText = 'font-size:12px;color:#666;text-align:center;margin-bottom:8px;min-height:14px;';

      const btnBase = 'width:100%;padding:11px;border-radius:7px;font-size:14px;font-weight:600;cursor:pointer;margin-bottom:8px;border:1px solid #ccc;';

      /* --- Step 1: Format --- */
      const formatPanel = document.createElement('div');
      formatPanel.setAttribute('data-export-panel', 'format');
      const fmtTitle = document.createElement('div');
      fmtTitle.textContent = 'Export format';
      fmtTitle.style.cssText = 'font-size:14px;font-weight:700;margin-bottom:10px;text-align:center;';
      const excelBtn = document.createElement('button');
      excelBtn.textContent = 'Excel (.xlsx)';
      excelBtn.style.cssText = btnBase + 'background:#4c00c7;color:#fff;border-color:#4c00c7;';
      excelBtn.onclick = () => {
        window._exportFormat = 'xlsx';
        scopeTitle.textContent = 'Excel — choose scope';
        _showExportPanel(c, 'scope');
        status.textContent = '';
      };
      const csvBtn = document.createElement('button');
      csvBtn.textContent = 'CSV';
      csvBtn.style.cssText = btnBase + 'background:#111;color:#fff;border-color:#111;';
      csvBtn.onclick = () => {
        window._exportFormat = 'csv';
        scopeTitle.textContent = 'CSV — choose scope';
        _showExportPanel(c, 'scope');
        status.textContent = '';
      };
      formatPanel.append(fmtTitle, excelBtn, csvBtn);

      /* --- Step 2: Scope --- */
      const scopePanel = document.createElement('div');
      scopePanel.setAttribute('data-export-panel', 'scope');
      scopePanel.style.display = 'none';
      const scopeTitle = document.createElement('div');
      scopeTitle.id = 'semrush-export-scope-title';
      scopeTitle.style.cssText = 'font-size:14px;font-weight:700;margin-bottom:10px;text-align:center;';
      const currentBtn = document.createElement('button');
      currentBtn.textContent = 'Current Page';
      currentBtn.style.cssText = btnBase + 'background:#111;color:#fff;border-color:#111;';
      currentBtn.onclick = async () => {
        status.textContent = 'Exporting current page...';
        await _expOne();
        status.textContent = '';
      };
      const multiBtn = document.createElement('button');
      multiBtn.textContent = 'Multiple Pages';
      multiBtn.style.cssText = btnBase + 'background:#4c00c7;color:#fff;border-color:#4c00c7;';
      multiBtn.onclick = () => {
        _showExportPanel(c, 'multi');
        status.textContent = '';
      };
      const backFmt = document.createElement('button');
      backFmt.textContent = '← Back';
      backFmt.style.cssText = btnBase + 'background:#fff;color:#555;';
      backFmt.onclick = () => _showExportPanel(c, 'format');
      scopePanel.append(scopeTitle, currentBtn, multiBtn, backFmt);

      /* --- Step 3: Multiple pages --- */
      const multiPanel = document.createElement('div');
      multiPanel.setAttribute('data-export-panel', 'multi');
      multiPanel.style.display = 'none';
      multiPanel.setAttribute('data-export-advanced-built', '1');

      const multiTitle = document.createElement('div');
      multiTitle.textContent = 'Multiple pages (parallel)';
      multiTitle.style.cssText = 'font-size:14px;font-weight:700;margin-bottom:8px;text-align:center;';
      const multiHint = document.createElement('div');
      multiHint.textContent = '15 workers run in parallel — as one page finishes, the next starts immediately.';
      multiHint.style.cssText = 'font-size:11px;color:#666;text-align:center;margin-bottom:10px;line-height:1.4;';

      const row = document.createElement('div');
      row.style.cssText = 'display:flex;gap:8px;margin-bottom:10px;align-items:center;justify-content:center;';
      const inp = document.createElement('input');
      inp.id = 'pg';
      inp.type = 'number';
      inp.value = 10;
      inp.min = 1;
      inp.max = 500;
      inp.style.cssText = 'width:70px;padding:8px;border-radius:6px;border:1px solid #ccc;font-size:14px;';
      const sp = document.createElement('span');
      sp.textContent = 'pages';
      sp.style.cssText = 'font-size:13px;color:#555;';
      row.append(inp, sp);

      const startBtn = document.createElement('button');
      startBtn.id = 'btn-exp-multi';
      startBtn.textContent = '⚡ Start Parallel Export';
      startBtn.style.cssText = btnBase + 'background:#4c00c7;color:#fff;border-color:#4c00c7;margin-bottom:6px;';
      startBtn.onclick = _startHybridExport;

      const stopBtn = document.createElement('button');
      stopBtn.id = 'btn-stop-exp';
      stopBtn.textContent = 'Stop & Save';
      stopBtn.style.cssText = btnBase + 'background:#fff;color:#c00;border-color:#c00;display:none;';
      stopBtn.onclick = () => { window._stopRequested = true; };

      const pr = document.createElement('div');
      pr.id = 'pr';
      pr.style.cssText = 'display:none;margin-top:6px;';
      const pt = document.createElement('div');
      pt.id = 'pt';
      pt.style.cssText = 'font-size:11px;color:#444;margin-bottom:5px;text-align:center;font-weight:600;';
      const pw = document.createElement('div');
      pw.style.cssText = 'height:5px;background:#e8e8e8;border-radius:4px;overflow:hidden;';
      const pb = document.createElement('div');
      pb.id = 'pb';
      pb.style.cssText = 'height:100%;width:0%;background:#4c00c7;transition:width .25s;';
      pw.appendChild(pb);
      pr.append(pt, pw);

      const backScope = document.createElement('button');
      backScope.textContent = '← Back';
      backScope.style.cssText = btnBase + 'background:#fff;color:#555;margin-top:4px;';
      backScope.onclick = () => _showExportPanel(c, 'scope');

      multiPanel.append(multiTitle, multiHint, row, startBtn, stopBtn, pr, backScope);
      c.append(closeBtn, status, formatPanel, scopePanel, multiPanel);

    // Position near export button
    if (anchorEl && anchorEl.getBoundingClientRect) {
      const r = anchorEl.getBoundingClientRect();
      c.style.top = Math.min(r.bottom + 8, window.innerHeight - 320) + 'px';
      c.style.left = Math.min(Math.max(8, r.right - 300), window.innerWidth - 300) + 'px';
      c.style.transform = 'none';
    } else {
      c.style.top = '50%';
      c.style.left = '50%';
      c.style.transform = 'translate(-50%,-50%)';
    }

    const scopeTitleEl = document.getElementById('semrush-export-scope-title');
    if (scopeTitleEl && window._exportFormat) {
      scopeTitleEl.textContent = (window._exportFormat === 'xlsx' ? 'Excel' : 'CSV') + ' — choose scope';
    }
    _showExportPanel(c, 'format');
    document.getElementById('semrush-export-status').textContent = '';
    c.style.display = 'block';
  }

  function getPageUrl(url, pageNum, pageSize) {
      let newUrl = url;
      
      // Auto-inject pagination parameters if they are completely missing in the captured URL
      if (!newUrl.includes('offset=') && !newUrl.includes('page=') && !newUrl.includes('pageIndex=') && !newUrl.includes('p=') && !newUrl.includes('from=')) {
          let rowsCount = document.querySelectorAll('[role="row"], tbody tr, tr, .sa-table__row, .srf-table__row, .ch-table__row').length - 1;
          let limit = (rowsCount > 60) ? 100 : 50;
          if (newUrl.includes('?')) {
              newUrl += '&offset=0&limit=' + limit;
          } else {
              newUrl += '?offset=0&limit=' + limit;
          }
          pageSize = limit;
      }
      
      // Extract pageSize if it exists in the URL
      let sizeMatch = newUrl.match(/(?:pageSize|limit|count)=(\d+)/);
      if (sizeMatch) {
          pageSize = parseInt(sizeMatch[1]);
      }
      
      // 1. If it has pageIndex (0-indexed)
      if (newUrl.includes('pageIndex=')) {
          newUrl = newUrl.replace(/pageIndex=\d+/, 'pageIndex=' + (pageNum - 1));
      }
      // 2. Semrush backlinks webapi2 uses display_page (0-indexed)
      else if (newUrl.includes('display_page=')) {
          newUrl = newUrl.replace(/display_page=\d+/, 'display_page=' + (pageNum - 1));
      }
      // 3. If it has page (1-indexed)
      else if (newUrl.includes('page=')) {
          newUrl = newUrl.replace(/page=\d+/, 'page=' + pageNum);
      }
      // 4. If it has p (1-indexed)
      else if (newUrl.includes('p=')) {
          newUrl = newUrl.replace(/p=\d+/, 'p=' + pageNum);
      }
      // 5. If it has offset
      else if (newUrl.includes('offset=')) {
          let offset = (pageNum - 1) * pageSize;
          newUrl = newUrl.replace(/offset=\d+/, 'offset=' + offset);
      }
      // 6. If it has from
      else if (newUrl.includes('from=')) {
          let offset = (pageNum - 1) * pageSize;
          newUrl = newUrl.replace(/from=\d+/, 'from=' + offset);
      }
      return newUrl;
  }

  function _isPositionsRpcMethod(method) {
    const m = String(method || '').toLowerCase();
    return m.includes('positions') && !m.includes('total');
  }

  function _prepareRpcPageBody(bodyStr, pageNum, pageSize) {
    if (typeof bodyStr !== 'string' || !bodyStr.trim()) return bodyStr;
    try {
      let parsed = JSON.parse(bodyStr);
      if (Array.isArray(parsed)) {
        const picked = parsed.find((item) => _isPositionsRpcMethod(item && item.method));
        if (picked) parsed = JSON.parse(JSON.stringify(picked));
        else parsed = JSON.parse(JSON.stringify(parsed[0]));
      }
      const updateParams = (obj) => {
        if (!obj || typeof obj !== 'object') return;
        if (Array.isArray(obj)) {
          obj.forEach((item) => updateParams(item));
          return;
        }
        if ('page' in obj) obj.page = pageNum;
        if ('pageIndex' in obj) obj.pageIndex = pageNum - 1;
        if ('offset' in obj) obj.offset = (pageNum - 1) * pageSize;
        if ('from' in obj) obj.from = (pageNum - 1) * pageSize;
        if ('display_page' in obj) obj.display_page = pageNum - 1;
        if ('pageSize' in obj) obj.pageSize = pageSize;
        Object.keys(obj).forEach((key) => {
          if (obj[key] && typeof obj[key] === 'object') updateParams(obj[key]);
        });
      };
      updateParams(parsed);
      return JSON.stringify(parsed);
    } catch (e) {
      return getPageBody(bodyStr, pageNum, pageSize);
    }
  }

  function getPageBody(bodyStr, pageNum, pageSize) {
      if (typeof bodyStr !== 'string') return bodyStr;
      try {
          let json = JSON.parse(bodyStr);
          let updated = false;
          
          const updateParams = (obj) => {
              if (!obj || typeof obj !== 'object') return;
              if (Array.isArray(obj)) {
                  obj.forEach(item => updateParams(item));
                  return;
              }
              
              if ('display_page' in obj) {
                  obj.display_page = pageNum - 1;
                  updated = true;
              }
              if ('offset' in obj) {
                  obj.offset = (pageNum - 1) * pageSize;
                  updated = true;
              }
              if ('pageIndex' in obj) {
                  obj.pageIndex = pageNum - 1;
                  updated = true;
              }
              if ('page' in obj) {
                  obj.page = pageNum;
                  updated = true;
              }
              if ('pageSize' in obj) {
                  obj.pageSize = pageSize;
                  updated = true;
              }
              if ('from' in obj) {
                  obj.from = (pageNum - 1) * pageSize;
                  updated = true;
              }
              
              for (let k in obj) {
                  if (obj[k] && typeof obj[k] === 'object') {
                      updateParams(obj[k]);
                  }
              }
          };
          
          updateParams(json);
          if (updated) {
              return JSON.stringify(json);
          }
      } catch(e) {}
      return bodyStr;
  }

  function jsonToCsv(jsonObj) {
      let arr = null;
      
      // Auto-unwrap JSON-RPC results array inside the first object if wrapped
      if (Array.isArray(jsonObj) && jsonObj.length === 1 && jsonObj[0] && typeof jsonObj[0] === 'object' && jsonObj[0].result) {
          jsonObj = jsonObj[0].result;
      }
      
      // Auto-unwrap standard JSON-RPC object
      if (jsonObj && typeof jsonObj === 'object' && !Array.isArray(jsonObj) && jsonObj.result) {
          jsonObj = jsonObj.result;
      }
      
      if (Array.isArray(jsonObj)) {
          arr = jsonObj;
      } else if (jsonObj && typeof jsonObj === 'object') {
          const preferredKeys = ['backlinks', 'refdomains', 'data', 'rows', 'items', 'result', 'list'];
          for (let i = 0; i < preferredKeys.length; i++) {
              const key = preferredKeys[i];
              if (Array.isArray(jsonObj[key]) && jsonObj[key].length) {
                  arr = jsonObj[key];
                  break;
              }
          }
          // Search keys for arrays
          if (!arr) for (let key in jsonObj) {
              if (Array.isArray(jsonObj[key])) {
                  arr = jsonObj[key];
                  break;
              }
          }
          if (!arr) {
              // Deeper search (one level)
              for (let key in jsonObj) {
                  if (jsonObj[key] && typeof jsonObj[key] === 'object') {
                      for (let subKey in jsonObj[key]) {
                          if (Array.isArray(jsonObj[key][subKey])) {
                              arr = jsonObj[key][subKey];
                              break;
                          }
                      }
                  }
                  if (arr) break;
              }
          }
      }
      
      if (!arr || arr.length === 0) return null;
      
      let keys = [];
      arr.forEach(item => {
          if (item && typeof item === 'object') {
              Object.keys(item).forEach(k => {
                  if (!keys.includes(k)) keys.push(k);
              });
          }
      });
      
      if (keys.length === 0) return null;
      
      let headers = keys;
      let rows = arr.map(item => {
          return keys.map(k => {
              let val = item[k];
              if (val === null || val === undefined) return '';
              if (typeof val === 'object') return JSON.stringify(val);
              return String(val);
          });
      });
      
      return { headers, rows };
  }

  async function _fetchExportPage(req, pageIdx) {
    const fetchUrl = _absoluteProxyUrl(req.url);
    const fetchInit = _sanitizeFetchInit(req.init || {}, req.method);
    try {
      if (req.rpcTemplate) {
        fetchInit.body = await _buildRpcPageBody(req.rpcTemplate, pageIdx, req.pageSize || 100);
      }
      const r = await fetch(fetchUrl, fetchInit);
      if (r.type === 'opaque' || r.status === 0) {
        throw new Error('Network blocked (status 0). Retrying with clean request headers.');
      }
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const text = await r.text();
      if (!text.trim()) throw new Error('Empty response received from API');
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed === 'object' && parsed.error) {
        throw new Error(parsed.error.message || 'RPC export error');
      }
      if (parsed && typeof parsed === 'object' && parsed.status && String(parsed.status).toLowerCase().includes('error')) {
        throw new Error(String(parsed.status));
      }
      console.log(`[API Export] Page ${pageIdx} fetched successfully.`);
      return parsed;
    } catch (err) {
      if (err instanceof SyntaxError) {
        console.error(`[API Export] Page ${pageIdx} parse error`);
        throw new Error('Response was not valid JSON.');
      }
      console.error(`[API Export] Page ${pageIdx} request failed (${fetchUrl}):`, err);
      return { __isFailedExportPage: true, pageIndex: pageIdx, errorMsg: err.message };
    }
  }

  async function fetchInBatches(requests, workerCount, onProgress) {
    const results = new Array(requests.length);
    let nextIndex = 0;
    let completed = 0;

    async function worker() {
      while (nextIndex < requests.length) {
        if (window._stopRequested) break;
        const idx = nextIndex++;
        const pageIdx = idx + 1;
        results[idx] = await _fetchExportPage(requests[idx], pageIdx);
        completed++;
        if (onProgress) onProgress(completed, requests.length);
      }
    }

    const poolSize = Math.max(1, Math.min(workerCount, requests.length));
    await Promise.all(Array.from({ length: poolSize }, () => worker()));
    return results;
  }

  function proxyUrl(url) {
      if (typeof url !== 'string') return url;
      let u = url.trim();
      if (u.startsWith('https://static.semrush.com')) {
          return u.replace('https://static.semrush.com', window.location.origin + '/static-proxy');
      }
      if (u.startsWith('http://static.semrush.com')) {
          return u.replace('http://static.semrush.com', window.location.origin + '/static-proxy');
      }
      if (u.startsWith('https://secure.semrush.com')) {
          return u.replace('https://secure.semrush.com', window.location.origin + '/secure-proxy');
      }
      if (u.startsWith('http://secure.semrush.com')) {
          return u.replace('http://secure.semrush.com', window.location.origin + '/secure-proxy');
      }
      if (u.startsWith('https://www.semrush.com')) {
          return u.replace('https://www.semrush.com', window.location.origin);
      }
      if (u.startsWith('http://www.semrush.com')) {
          return u.replace('http://www.semrush.com', window.location.origin);
      }
      if (u.startsWith('//www.semrush.com')) {
          return window.location.protocol + u.replace('//www.semrush.com', '//' + window.location.host);
      }
      return u;
  }

  function isCapturedRequestRelevant() {
      if (!window._lastTableApiRequest) return false;
      const path = window.location.pathname.toLowerCase();
      const url = String(window._lastTableApiRequest.url || '').toLowerCase();
      const body = String(window._lastTableApiRequest.body || (window._lastTableApiRequest.init && window._lastTableApiRequest.init.body) || '').toLowerCase();
      
      if (path.includes('/refdomains') && (url.includes('refdomains') || url.includes('referring') || url.includes('webapi2'))) return true;
      if (path.includes('/backlinks') && (url.includes('backlinks') || url.includes('backlink') || url.includes('webapi2'))) return true;
      if ((path.includes('/adwords') || path.includes('/paid')) && url.includes('/rpc')) {
          return body.includes('adwords.positions') || body.includes('adwords.');
      }
      if ((path.includes('/organic/positions') || path.includes('/organic')) && url.includes('/rpc')) {
          return body.includes('organic') || body.includes('positions');
      }
      if (path.includes('/keyword') && (url.includes('keyword') || url.includes('phrase') || url.includes('kw'))) return true;
      if (path.includes('/gap') && url.includes('gap')) return true;
      
      return false;
  }

  function _extractWebapi2Key() {
    if (window._lastTableApiRequest && window._lastTableApiRequest.url) {
      const fromCaptured = String(window._lastTableApiRequest.url).match(/[?&]key=([a-f0-9]{20,})/i);
      if (fromCaptured) return fromCaptured[1];
    }
    try {
      const entries = performance.getEntriesByType('resource');
      for (let i = entries.length - 1; i >= 0; i--) {
        const m = String(entries[i].name || '').match(/[?&]key=([a-f0-9]{20,})/i);
        if (m) return m[1];
      }
    } catch (e) {}
    const html = document.documentElement.innerHTML;
    const inline = html.match(/webapi2[^"'\\s]*[?&]key=([a-f0-9]{20,})/i) || html.match(/"key"\s*:\s*"([a-f0-9]{20,})"/i);
    return inline ? inline[1] : null;
  }

  function tryReconstructRequest() {
      // If there is a captured request, but it belongs to a different tool context, clear it
      if (window._lastTableApiRequest && !isCapturedRequestRelevant()) {
          window._lastTableApiRequest = null;
      }

      if (window._lastTableApiRequest) {
          const capturedUrl = String(window._lastTableApiRequest.url || '');
          if (capturedUrl.includes('/api/backlinks/') && !capturedUrl.includes('webapi2')) {
              window._lastTableApiRequest = null;
          } else {
              return;
          }
      }
      
      let path = window.location.pathname.toLowerCase();
      let searchParams = new URLSearchParams(window.location.search);
      const target = searchParams.get('q') || searchParams.get('domain') || '';
      if (!target) return;

      const key = _extractWebapi2Key();
      if (!key) return;

      const targetType = searchParams.get('searchType') === 'domain' ? 'root_domain' : (searchParams.get('target_type') || 'root_domain');
      
      if (path.includes('/refdomains')) {
          window._lastTableApiRequest = {
              url: `/analytics/backlinks/webapi2/?action=report&key=${key}&type=refdomains&target=${encodeURIComponent(target)}&target_type=${encodeURIComponent(targetType)}&display_page=0&sort_field=domain_ascore&sort_type=desc`,
              init: { method: 'GET' }
          };
          console.log('Auto-reconstructed Referring Domains webapi2 URL:', window._lastTableApiRequest.url);
      }
      else if (path.includes('/backlinks')) {
          window._lastTableApiRequest = {
              url: `/analytics/backlinks/webapi2/?action=report&key=${key}&type=backlinks&target=${encodeURIComponent(target)}&target_type=${encodeURIComponent(targetType)}&display_page=0&sort_field=page_ascore&sort_type=desc`,
              init: { method: 'GET' }
          };
          console.log('Auto-reconstructed Backlinks webapi2 URL:', window._lastTableApiRequest.url);
      }
  }

  async function _expApiParallel() {
      tryReconstructRequest();
      if (!window._lastTableApiRequest || !isCapturedRequestRelevant()) {
          console.warn("No relevant captured table API request.");
          return false;
      }
      
      const p = Math.min(Math.max(+document.getElementById('pg').value || 1, 1), 500);
      const pr = document.getElementById('pr');
      const pb = document.getElementById('pb');
      const pt = document.getElementById('pt');
      if (pr) pr.style.display = 'block';
      if (pb) pb.style.width = '0%';
      if (pt) pt.textContent = `Parallel fetch: 0 / ${p} pages (15 workers)...`;
      
      try {
          let reqInfo = window._lastTableApiRequest;
          const capturedBody = reqInfo.body || (reqInfo.init && reqInfo.init.body) || null;
          let url = proxyUrl(reqInfo.url);
          const method = reqInfo.method || (reqInfo.init && reqInfo.init.method) || 'GET';
          
          let pageSize = 100;
          if (url.includes('display_page=')) {
              pageSize = 100;
          } else {
              let sizeMatch = url.match(/(?:pageSize|limit|count)=(\d+)/);
              if (sizeMatch) {
                  pageSize = parseInt(sizeMatch[1], 10);
              } else if (capturedBody) {
                  try {
                      const bodyObj = JSON.parse(String(capturedBody));
                      const findPageSize = (obj) => {
                          if (!obj || typeof obj !== 'object') return 0;
                          if (typeof obj.pageSize === 'number') return obj.pageSize;
                          if (obj.display && typeof obj.display.pageSize === 'number') return obj.display.pageSize;
                          if (obj.params && obj.params.display && typeof obj.params.display.pageSize === 'number') {
                              return obj.params.display.pageSize;
                          }
                          if (Array.isArray(obj)) {
                              for (let i = 0; i < obj.length; i++) {
                                  const found = findPageSize(obj[i]);
                                  if (found) return found;
                              }
                          } else {
                              for (const key in obj) {
                                  const found = findPageSize(obj[key]);
                                  if (found) return found;
                              }
                          }
                          return 0;
                      };
                      const fromBody = findPageSize(bodyObj);
                      if (fromBody) pageSize = fromBody;
                  } catch (e) {}
              }
              if (pageSize === 100) {
                  let rowsCount = document.querySelectorAll('[role="row"], tbody tr, tr, .sa-table__row, .srf-table__row, .ch-table__row').length - 1;
                  if (rowsCount > 0) pageSize = (rowsCount > 60) ? 100 : 50;
              }
          }
          
          let isPost = String(method).toUpperCase() === 'POST';
          let batchSize = 15;
          const isRpcExport = !!(capturedBody && String(capturedBody).includes('"jsonrpc"'));
          
          let requests = [];
          for (let i = 1; i <= p; i++) {
              let pageUrl = getPageUrl(url, i, pageSize);
              const rawInit = Object.assign({}, reqInfo.init || {});
              if (reqInfo.headers) {
                  rawInit.headers = Object.assign({}, rawInit.headers || {}, reqInfo.headers);
              }
              let pageInit = _sanitizeFetchInit(rawInit, method);
              if (capturedBody && !isRpcExport) {
                  pageInit.body = _prepareRpcPageBody(String(capturedBody), i, pageSize);
              }
              requests.push({
                  url: pageUrl,
                  init: pageInit,
                  method: method,
                  rpcTemplate: isRpcExport ? capturedBody : null,
                  pageSize: pageSize
              });
          }
          
          // Fetch with a 15-worker pool: always 15 in flight, next page starts when any worker frees up
          let results = await fetchInBatches(requests, batchSize, (completed, total) => {
              let percent = Math.round((completed / total) * 100);
              if (pb) pb.style.width = percent + '%';
              if (pt) pt.textContent = `Fetched ${completed} of ${total} pages... (${percent}%)`;
          });
          
          let allRows = [];
          let finalHeaders = [];
          let failedPages = [];
          let errorDetails = "";
          
          results.forEach((jsonObj, index) => {
              const pageNum = index + 1;
              if (!jsonObj) {
                  failedPages.push(pageNum);
                  return;
              }
              if (jsonObj.__isFailedExportPage) {
                  failedPages.push(jsonObj.pageIndex);
                  if (!errorDetails && jsonObj.errorMsg) {
                      errorDetails = jsonObj.errorMsg;
                  }
                  return;
              }
              let csvData = jsonToCsv(jsonObj);
              if (csvData) {
                  if (finalHeaders.length === 0) {
                      finalHeaders = csvData.headers;
                  }
                  allRows.push(...csvData.rows);
              } else {
                  console.warn(`[API Export] Page ${pageNum} parsed JSON but structure was empty/not a table.`);
                  failedPages.push(pageNum);
              }
          });
          
          if (failedPages.length > 0) {
              console.warn(`[API Export] Failed to export pages: ${failedPages.join(', ')}`);
              const errToShow = errorDetails ? ` (${errorDetails})` : "";
              if (pt) pt.innerHTML = `<span style="color:#f59e0b;">⚠️ API method partially failed${errToShow}. Pages: ${failedPages.join(',')}. Falling back to click...</span>`;
              return false;
          }
          
          if (allRows.length > 0) {
              // Apply official limit of maximum 30,000 rows
              if (allRows.length > 30000) {
                  allRows = allRows.slice(0, 30000);
                  alert("⚠️ Maximum Limit Reached: Export limited to 30,000 rows to ensure optimal performance (Official Semrush Limit).");
              }
              
              if (window._exportFormat === 'xlsx') {
                  await _dl(finalHeaders, allRows, `semrush-api-export-${allRows.length}.xlsx`);
              } else {
                  let csvContent = [
                      finalHeaders.map(h => `"${h.replace(/"/g, '""')}"`).join(','),
                      ...allRows.map(r => r.map(v => `"${v.replace(/"/g, '""')}"`).join(','))
                  ].join('\r\n');
                  _dlCsv(csvContent, `semrush-api-export-${allRows.length}.csv`);
              }
              document.getElementById('semrush-export-container').style.display = 'none';
              if (pr) pr.style.display = 'none';
              return true;
          } else {
              console.warn("Parallel Export Error: Could not extract rows from API response JSON.");
              return false;
          }
      } catch (err) {
          console.error("Parallel API Export Error: ", err);
          return false;
      }
  }

  async function _startHybridExport() {
      window._stopRequested = false;
      document.getElementById('btn-exp-multi').style.display = 'none';
      document.getElementById('btn-stop-exp').style.display = 'block';

      // 2. Try the Parallel API Exporter
      const success = await _expApiParallel();
      if (!success) {
          console.warn("Parallel API Export failed or was not captured. Falling back to Simulated Click export...");
          const pt = document.getElementById('pt');
          if (pt && !pt.innerHTML.includes("failed")) {
              pt.textContent = "⚠️ Parallel method failed. Switching to Simulated Click...";
          }
          await new Promise(r => setTimeout(r, 3000)); // Pause so the user is informed
          await _expMulti();
      } else {
          // Parallel succeeded, reset buttons
          document.getElementById('btn-exp-multi').style.display = 'block';
          document.getElementById('btn-stop-exp').style.display = 'none';
      }
  }

  /* ================= SITE AUDIT ISSUES LIST EXPORT ================= */
  function _isSiteAuditIssuesList() {
    return !!document.querySelector('li[data-test-id^="issues-list-item-"]');
  }

  function _expSiteAuditIssues() {
    const SEVERITY = { errors: 'Error', warnings: 'Warning', notices: 'Notice' };
    const headers = ['Severity', 'Issue Title', 'Count', 'Detail URL', 'New Issues', 'New Issues URL'];
    const rows = [];

    document.querySelectorAll('li[data-test-id^="issues-list-item-"]').forEach(item => {
      const severityKey = item.getAttribute('data-test-id').replace('issues-list-item-', '');
      const severity = SEVERITY[severityKey] || severityKey;

      const titleEl = item.querySelector('[data-test-id="issue-title"]');
      if (!titleEl) return;

      const titleLink = titleEl.querySelector('a[href]');
      const countText = titleLink ? titleLink.textContent.trim() : '';
      const count = countText.split(' ')[0];
      const fullTitle = (titleEl.innerText || titleEl.textContent).trim().replace(/\s+/g, ' ');

      let detailUrl = '';
      if (titleLink) {
        const href = titleLink.getAttribute('href');
        detailUrl = href.startsWith('/') ? window.location.origin + href : href;
      }

      const newLink = item.querySelector('[data-test-id="issues-list-item-new-links"]');
      const newCount = newLink ? newLink.textContent.trim().split(' ')[0] : '';
      let newUrl = '';
      if (newLink) {
        const href = newLink.getAttribute('href');
        newUrl = href.startsWith('/') ? window.location.origin + href : href;
      }

      rows.push([severity, fullTitle, count, detailUrl, newCount, newUrl]);
    });

    return { headers, rows };
  }

  /* ================= EXPORT LOGIC ================= */
  const ROW_SEL = '[role="row"], tbody tr, tr, .sa-table__row, .srf-table__row, .ch-table__row, .srf-table__tr, [data-ui-name*="Row"], [data-test-id*="row"], .ReactVirtualized__Table__row';

  function _tbl() {
    const s = [
      '[role="grid"]', '[role="table"]', 'table', '.sa-table', '.srf-table', 
      '[data-ui-name*="Table"]', '[data-test-id*="table"]', '.ReactVirtualized__Table', '.ch-table'
    ];
    for (const k of s) {
      const t = document.querySelectorAll(k);
      if (t.length)
        return [...t].reduce((a, b) => {
          return b.querySelectorAll(ROW_SEL).length > a.querySelectorAll(ROW_SEL).length ? b : a;
        });
    }
    return null;
  }

  const _getValidColumns = t => {
    const allH = [...t.querySelectorAll('[role="columnheader"], thead th, th, .sa-table__header-cell, .srf-table__header-cell, .ch-table__header-cell, .srf-table__th')];
    const topH = allH.filter(el => !allH.some(p => p !== el && p.contains(el)));
    
    const headers = topH.map(x => (x.innerText || x.textContent).trim().replace(/\n+/g, ' '));
    const indices = [];
    headers.forEach((txt, i) => {
      // Keep column if it's not empty, not ILR, not LR, not checkbox
      if (txt && txt !== 'ILR' && txt !== 'LR' && txt !== 'Symbol(SELECT_ALL)') {
        indices.push(i);
      }
    });
    return { topH, headers, indices };
  };

  const _rows = (t, topH, indices) => {
    return [...t.querySelectorAll(ROW_SEL)]
      .filter(r => !r.querySelector('th, [role="columnheader"], .sa-table__header-cell'))
      .map(r => {
        const allC = [...r.querySelectorAll('[role="gridcell"], [role="cell"], td, .sm-table-layout__cell, .sa-table__cell, .srf-table__cell, .ch-table__cell, .srf-table__td, .ReactVirtualized__Table__rowColumn')];
        let cells = allC.filter(el => !allC.some(p => p !== el && p.contains(el)));

        // Try to map by aria-colindex if available
        const mappedCells = [];
        if (topH[0] && topH[0].hasAttribute('aria-colindex') && cells.some(c => c.hasAttribute('aria-colindex'))) {
           topH.forEach((h, i) => {
             const idx = h.getAttribute('aria-colindex');
             const matchingCell = cells.find(c => c.getAttribute('aria-colindex') === idx);
             mappedCells[i] = matchingCell || null;
           });
        } else {
           // If no aria-colindex, prevent extra inner cells from shifting columns
           if (cells.length > topH.length) {
              const strictSels = ['[role="gridcell"]', '[role="cell"]', 'td', '.ReactVirtualized__Table__rowColumn'];
              for (const sel of strictSels) {
                 const strictCells = [...r.querySelectorAll(sel)];
                 if (strictCells.length === topH.length) {
                    cells = strictCells;
                    break;
                 }
              }
           }
           topH.forEach((_, i) => mappedCells[i] = cells[i] || null);
        }

        return indices.map(i => {
          const c = mappedCells[i];
          if (!c) return '';
          
          let text = (c.innerText || '').trim();
          if (!text) {
             text = (c.textContent || '').trim().replace(/\s+/g, ' ');
          } else {
             text = text.replace(/\n+/g, ' - ');
          }
          
          let links = [...c.querySelectorAll('a[href]')].map(a => {
            let h = a.getAttribute('href');
            return h.startsWith('/') ? window.location.origin + h : h;
          });
          links = [...new Set(links)].filter(l => !l.includes('javascript:'));
          
          let externalLinks = links.filter(l => !l.includes('/siteaudit/campaign/'));
          let extraLinks = externalLinks.length > 0 ? externalLinks : links;
          
          let newLinks = extraLinks.filter(l => !text.includes(l));
          if (newLinks.length > 0) {
            if (text) {
              const url = newLinks[0].replace(/"/g, "'");
              const label = text.replace(/"/g, "'");
              text = `=HYPERLINK("${url}","${label}")`;
            } else {
              text = newLinks[0];
            }
          }
          return text;
        });
      })
      .filter(r => r.some(v => v));
  };

  const _csv = (h, r) =>
    [h, ...r]
      .map(x => x.map(v => `"${v.replace(/"/g, '""')}"`).join(','))
      .join('\r\n');

  const _dlCsv = (content, name) => {
    const bom = '\uFEFF';
    _downloadBlob(new Blob([bom + content], { type: 'text/csv;charset=utf-8;' }), name);
  };

  const _dl = async (headers, rows, filename) => {
    const fmt = window._exportFormat || 'csv';
    const name = filename || ('semrush-export.' + (fmt === 'xlsx' ? 'xlsx' : 'csv'));
    if (fmt === 'xlsx') await _exportAsXlsx(headers, rows, name);
    else await _exportAsCsv(headers, rows, name);
  };

  async function _next() {
    const s = [
      '[data-ui-name="Pagination.NextPage"]',
      '[data-test-pagination-next-btn]',
      '[data-test-id="pagination-next"]',
      '[data-test-id="pagination-next-button"]',
      '[data-test="pagination-next"]',
      '[data-at="pagination-next"]',
      'a[aria-label="Next page"]',
      'button[aria-label="Next page"]',
      '[aria-label*="next" i]',
      '.srf-pagination__item--next',
      '.srf-pagination__next',
      '.pagination-next'
    ];
    for (const k of s) {
      const b = document.querySelector(k);
      if (b) {
        const isDisabled = b.disabled ||
          b.getAttribute('aria-disabled') === 'true' ||
          b.classList.contains('srf-pagination__item--disabled') ||
          b.classList.contains('disabled');
        if (!isDisabled) {
          b.click();
          return true;
        }
      }
    }
    return false;
  }

  async function _expOne() {
    try {
      if (_isSiteAuditIssuesList()) {
        const { headers, rows } = _expSiteAuditIssues();
        if (rows.length) {
          await _dl(headers, rows, 'semrush-site-audit-issues.' + (window._exportFormat === 'xlsx' ? 'xlsx' : 'csv'));
          document.getElementById('semrush-export-container').style.display = 'none';
        } else {
          alert("No issues found to export.");
        }
        return;
      }
      const t = _tbl();
      if (!t) return alert("No table found on this page to export.");
      const { topH, headers, indices } = _getValidColumns(t);
      const finalHeaders = indices.map(i => headers[i]);
      const r = _rows(t, topH, indices);
      if (r.length) {
        await _dl(finalHeaders, r, 'semrush-export.' + (window._exportFormat === 'xlsx' ? 'xlsx' : 'csv'));
        document.getElementById('semrush-export-container').style.display = 'none';
      } else {
        alert("Error: Table detected, but couldn't find any rows inside it. Semrush might be using a new layout on this page.");
      }
    } catch (err) {
      alert("Export Error: " + err.message);
    }
  }

  async function _expMulti() {
    try {
      window._stopRequested = false;
      document.getElementById('btn-exp-multi').style.display = 'none';
      document.getElementById('btn-stop-exp').style.display = 'block';
      const p = Math.min(Math.max(+document.getElementById('pg').value || 1, 1), 500);
      const pr = document.getElementById('pr');
      const pb = document.getElementById('pb');
      const pt = document.getElementById('pt');
      if (pr) pr.style.display = 'block';
      _xS = { h: [], r: [], topH: [], indices: [] };

      for (let i = 1; i <= p; i++) {
        // Wait up to 5s for table to be present in DOM
        let t = _tbl();
        let retries = 10;
        while (!t && retries > 0) {
            await new Promise(r => setTimeout(r, 500));
            t = _tbl();
            retries--;
        }
        
        if (!t) {
            console.warn(`[Simulated Click] No table found on page ${i}`);
            if (pt) pt.innerHTML = `<span style="color:#ef4444;">Page ${i}: Table element not found</span>`;
            break;
        }
        
        if (i === 1) {
          const { topH, headers, indices } = _getValidColumns(t);
          _xS.h = indices.map(idx => headers[idx]);
          _xS.topH = topH;
          _xS.indices = indices;
        }
        
        const rowsFound = _rows(t, _xS.topH, _xS.indices);
        _xS.r.push(...rowsFound);

        console.log(`[Simulated Click] Page ${i}: Scraped ${rowsFound.length} rows. Total: ${_xS.r.length}`);

        if (pt) pt.textContent = `Exporting Page ${i} of ${p}... (${_xS.r.length} rows collected)`;
        if (pb) pb.style.width = (i / p) * 100 + '%';

        if (i < p) {
          if (window._stopRequested) break;
          
          const lastFirstRowText = rowsFound[0]?.join(',');

          // Wait up to 5s for Next button to be enabled before clicking
          let clicked = false;
          let clickRetries = 10;
          while (clickRetries > 0) {
              clicked = await _next();
              if (clicked) break;
              await new Promise(r => setTimeout(r, 500));
              clickRetries--;
          }

          if (!clicked) {
              console.warn(`[Simulated Click] Next button disabled/not clickable. Stopping at page ${i}.`);
              if (pt) pt.textContent = `Last page reached or next button disabled.`;
              break;
          }

          // Wait up to 10 seconds for page to transition (first row changes)
          let loaded = false;
          for (let attempt = 0; attempt < 20; attempt++) {
              await new Promise(r => setTimeout(r, 500));
              const newT = _tbl();
              if (newT) {
                  const newRows = _rows(newT, _xS.topH, _xS.indices);
                  const newFirstRowText = newRows[0]?.join(',');
                  if (newFirstRowText && newFirstRowText !== lastFirstRowText) {
                      console.log(`[Simulated Click] Page ${i+1} transition loaded in ${(attempt+1)*500}ms.`);
                      loaded = true;
                      break;
                  }
              }
          }
          if (!loaded) {
              console.warn(`[Simulated Click] Table content did not change after 10s. Proceeding anyway...`);
          }
          
          if (window._stopRequested) break;
        }
      }

      if (_xS.r.length) {
        await _dl(_xS.h, _xS.r, `semrush-export-${_xS.r.length}.` + (window._exportFormat === 'xlsx' ? 'xlsx' : 'csv'));
      } else {
        alert("No data found to export.");
      }
    } catch (err) {
      alert("Export Error: " + err.message);
    } finally {
      if (pr) pr.style.display = 'none';
      document.getElementById('btn-exp-multi').style.display = 'block';
      document.getElementById('btn-stop-exp').style.display = 'none';
      document.getElementById('semrush-export-container').style.display = 'none';
    }
  }

  /* ================= HIJACK EXPORT BUTTON ================= */
  function hijackExport(e) {
    if (window._semrushPdfExportRunning) return;
    const target = e.target;
    if (!target || !target.closest) return;
    if (target.closest('#semrush-export-container, #semrush-pdf-progress')) return;

    const btnEl = _findExportButton(target);
    if (!btnEl) return;

    e.preventDefault();
    e.stopPropagation();
    e.stopImmediatePropagation();

    _hideOfficialModals();

    if (_isPdfBtn(btnEl) || (btnEl.textContent || '').toLowerCase().includes('download pdf')) {
      window._semrushLastExportIntent = 'pdf';
      _exportPdf();
      return;
    }

    window._semrushLastExportIntent = 'table';
    _uiFormatPicker(btnEl);
  }

  // Intercept multiple event types because some React dropdowns open on mousedown/pointerdown
  HIJACK_EVENTS.forEach((type) => {
    document.addEventListener(type, hijackExport, true);
  });
  
})();
