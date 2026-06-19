// aria2.js — shared aria2 JSON-RPC integration used by both the admin resolve
// page and the CDK user portal. Exposes window.Aria2 with config persisted in
// this browser's localStorage, a config modal, and helpers that build the
// "push to aria2" / "aria2 config" buttons the pages mount into their UI.
//
// The push goes straight from the browser to the user's own aria2 RPC endpoint
// (default http://localhost:6800/jsonrpc), so aria2 must run with RPC enabled
// and cross-origin allowed (--enable-rpc --rpc-allow-origin-all). Nothing about
// the endpoint is sent to this tool's backend.
(function () {
  'use strict';

  const STORAGE_KEY = 'pikpak.aria2.config';

  const ICON_PUSH =
    '<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 3 3 10.5l6.2 2.3L21 3Z"/><path d="m21 3-7.7 18-2.3-6.2"/></svg>';
  const ICON_GEAR =
    '<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>';
  const ICON_CLOSE =
    '<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>';

  function defaults() {
    return { rpcUrl: 'http://localhost:6800/jsonrpc', token: '', dir: '' };
  }

  function loadConfig() {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return defaults();
      return Object.assign(defaults(), JSON.parse(raw));
    } catch (e) {
      return defaults();
    }
  }

  function saveConfig(cfg) {
    const clean = {
      rpcUrl: String(cfg.rpcUrl || '').trim(),
      token: String(cfg.token || '').trim(),
      dir: String(cfg.dir || '').trim(),
    };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(clean));
    return clean;
  }

  function isConfigured() {
    return Boolean(loadConfig().rpcUrl);
  }

  // normalizeRpcUrl fills in the JSON-RPC path users commonly omit. aria2 serves
  // its RPC at "/jsonrpc", so a bare "http://host:6800" (no path) would POST to
  // "/" and get a 404. When the address has no path (or just "/"), default it to
  // "/jsonrpc"; any explicit path the user typed is left untouched.
  function normalizeRpcUrl(raw) {
    const value = String(raw || '').trim();
    if (!value) return '';
    try {
      const u = new URL(value);
      if (u.pathname === '' || u.pathname === '/') {
        u.pathname = '/jsonrpc';
      }
      return u.toString();
    } catch (e) {
      // Not a parseable absolute URL — hand it back and let fetch report it.
      return value;
    }
  }

  // rpc issues a single aria2 JSON-RPC call over HTTP POST. The secret token, if
  // set, is passed as the conventional "token:<secret>" first parameter.
  async function rpc(method, params, cfg) {
    cfg = cfg || loadConfig();
    const url = normalizeRpcUrl(cfg.rpcUrl);
    if (!url) {
      throw new Error('请先配置 aria2 RPC 地址');
    }
    const args = [];
    if (cfg.token) args.push('token:' + cfg.token);
    for (const p of params || []) args.push(p);

    let response;
    try {
      response = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          jsonrpc: '2.0',
          id: 'pikpak-' + Date.now(),
          method,
          params: args,
        }),
      });
    } catch (e) {
      // A network/CORS/mixed-content failure lands here with no useful status.
      throw new Error('无法连接 aria2，请检查 RPC 地址、aria2 是否已开启 RPC 并允许跨域');
    }

    let payload = null;
    try {
      payload = await response.json();
    } catch (e) {
      payload = null;
    }
    if (!response.ok) {
      if (response.status === 404) {
        throw new Error('aria2 请求失败 (404)：地址未找到，请确认 RPC 地址指向 aria2 的 JSON-RPC 接口（通常以 /jsonrpc 结尾）');
      }
      throw new Error(payload?.error?.message || `aria2 请求失败 (${response.status})`);
    }
    if (payload?.error) {
      throw new Error(payload.error.message || 'aria2 返回错误');
    }
    return payload?.result;
  }

  // sanitizeOutPath turns a resolved file's relative path into a safe value for
  // aria2's "out" option. aria2 writes the file to <dir>/<out>, will create
  // sub-directories from any "/" in out and even follow "../" out of the download
  // dir, and on Windows silently truncates a name at an illegal character such as
  // ':' or '?'. So we keep "/" as a sub-directory separator but, per segment, drop
  // ""/"."/".." (so a crafted share name can't escape the dir), replace
  // filesystem-reserved characters with "_", and trim trailing dots/spaces that
  // Windows forbids. Returns "" when nothing usable remains.
  function sanitizeOutPath(raw) {
    // Windows treats these as device names (a file called "NUL" silently discards
    // its bytes — aria2 reports success but writes nothing), so prefix any segment
    // that is one, with or without an extension.
    const reserved = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i;
    const segments = String(raw == null ? '' : raw).replace(/\\/g, '/').split('/');
    const safe = [];
    for (let seg of segments) {
      seg = seg.replace(/[\x00-\x1f<>:"|?*]/g, '_').replace(/[ .]+$/, '').trim();
      if (seg === '' || seg === '.' || seg === '..') continue;
      if (reserved.test(seg)) seg = '_' + seg;
      safe.push(seg);
    }
    return safe.join('/');
  }

  // addUri pushes one download URL to aria2. The name argument is the file's
  // relative path; it becomes aria2's "out" option (sanitized) so the saved file
  // keeps its real name and, for a file inside a resolved folder, its
  // sub-directory — which also stops same-named files in different folders from
  // overwriting each other. The configured dir (if any) becomes "dir".
  function addUri(url, name, cfg) {
    cfg = cfg || loadConfig();
    const options = {};
    if (cfg.dir) options.dir = cfg.dir;
    const out = sanitizeOutPath(name);
    if (out) options.out = out;
    return rpc('aria2.addUri', [[url], options], cfg);
  }

  // pushOne sends a single link and surfaces the outcome as a toast.
  async function pushOne(url, name) {
    if (!url) {
      toast('链接为空，无法推送', 'error');
      return false;
    }
    if (!isConfigured()) {
      toast('请先配置 aria2', 'error');
      openConfig();
      return false;
    }
    try {
      await addUri(url, name);
      toast('已推送到 aria2' + (name ? '：' + name : ''), 'success');
      return true;
    } catch (e) {
      toast(e.message, 'error');
      return false;
    }
  }

  // pushMany sends a list of {url, name} items, reporting an aggregate result.
  let pushManyBusy = false;

  async function pushMany(items) {
    const list = (items || []).filter((it) => it && it.url);
    if (list.length === 0) {
      toast('没有可推送的链接', 'error');
      return;
    }
    if (!isConfigured()) {
      toast('请先配置 aria2', 'error');
      openConfig();
      return;
    }
    if (pushManyBusy) return;

    pushManyBusy = true;
    showPushOverlay(list.length);
    let ok = 0;
    const failures = [];
    try {
      for (let i = 0; i < list.length; i++) {
        const it = list[i];
        updatePushOverlay(i, list.length, it.name);
        try {
          await addUri(it.url, it.name);
          ok += 1;
        } catch (e) {
          failures.push(e.message);
        }
        updatePushOverlay(i + 1, list.length, it.name);
      }
    } finally {
      await hidePushOverlay();
      pushManyBusy = false;
    }
    if (failures.length === 0) {
      toast(`已推送 ${ok} 个链接到 aria2`, 'success');
    } else if (ok > 0) {
      toast(`已推送 ${ok} 个，${failures.length} 个失败`, 'info');
    } else {
      toast('推送失败：' + (failures[0] || '未知错误'), 'error');
    }
  }

  let pushOverlay = null;

  function buildPushOverlay() {
    if (pushOverlay) return pushOverlay;
    pushOverlay = document.createElement('div');
    pushOverlay.className = 'aria2-push-overlay hidden';
    pushOverlay.setAttribute('role', 'status');
    pushOverlay.setAttribute('aria-live', 'polite');
    pushOverlay.setAttribute('aria-busy', 'false');
    pushOverlay.innerHTML = `
      <div class="aria2-push-panel">
        <div class="aria2-push-spinner" aria-hidden="true"></div>
        <div class="aria2-push-copy">
          <h2>正在推送到 aria2</h2>
          <p class="aria2-push-progress">准备中</p>
          <p class="aria2-push-detail">请保持此页面打开</p>
        </div>
      </div>`;
    document.body.appendChild(pushOverlay);
    return pushOverlay;
  }

  function pushOverlayEls() {
    buildPushOverlay();
    return {
      progress: pushOverlay.querySelector('.aria2-push-progress'),
      detail: pushOverlay.querySelector('.aria2-push-detail'),
    };
  }

  function showPushOverlay(total) {
    const els = pushOverlayEls();
    els.progress.textContent = `0 / ${total}`;
    els.detail.textContent = '正在连接 aria2...';
    pushOverlay.classList.remove('closing');
    pushOverlay.classList.remove('hidden');
    pushOverlay.setAttribute('aria-busy', 'true');
  }

  function updatePushOverlay(done, total, name) {
    if (!pushOverlay || pushOverlay.classList.contains('hidden')) return;
    const els = pushOverlayEls();
    els.progress.textContent = `${done} / ${total}`;
    const cleanName = String(name || '').trim();
    els.detail.textContent = cleanName ? `正在推送：${cleanName}` : '正在推送链接...';
  }

  function hidePushOverlay() {
    if (!pushOverlay || pushOverlay.classList.contains('hidden')) {
      return Promise.resolve();
    }
    pushOverlay.setAttribute('aria-busy', 'false');
    pushOverlay.classList.add('closing');
    return new Promise((resolve) => {
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        pushOverlay.classList.add('hidden');
        pushOverlay.classList.remove('closing');
        resolve();
      };
      pushOverlay.addEventListener('animationend', function handler(e) {
        if (e.target !== pushOverlay) return;
        pushOverlay.removeEventListener('animationend', handler);
        finish();
      });
      window.setTimeout(finish, 320);
    });
  }

  // --- Config modal ---

  let overlay = null;

  function buildModal() {
    if (overlay) return overlay;
    overlay = document.createElement('div');
    overlay.className = 'aria2-overlay hidden';
    overlay.innerHTML = `
      <div class="aria2-modal" role="dialog" aria-modal="true" aria-label="aria2 配置">
        <div class="aria2-modal-head">
          <h2>aria2 推送配置</h2>
          <button type="button" class="aria2-close" aria-label="关闭">${ICON_CLOSE}</button>
        </div>
        <p class="muted aria2-modal-hint">配置保存在本浏览器本地。aria2 需开启 RPC 并允许跨域：<code>--enable-rpc --rpc-allow-origin-all</code></p>
        <label class="field floating aria2-field">
          <input id="aria2RpcUrl" type="text" autocomplete="off" placeholder=" ">
          <span class="floating-label">RPC 地址（如 http://localhost:6800/jsonrpc）</span>
        </label>
        <label class="field floating aria2-field">
          <input id="aria2Token" type="text" autocomplete="off" placeholder=" ">
          <span class="floating-label">RPC 密钥 Secret（可选）</span>
        </label>
        <label class="field floating aria2-field">
          <input id="aria2Dir" type="text" autocomplete="off" placeholder=" ">
          <span class="floating-label">下载目录（可选，留空用 aria2 默认）</span>
        </label>
        <p class="status-error hidden aria2-error"></p>
        <div class="aria2-modal-actions">
          <button type="button" class="secondary aria2-test">测试连接</button>
          <button type="button" class="primary aria2-save">保存</button>
        </div>
      </div>`;
    document.body.appendChild(overlay);

    const close = () => hideModal();
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });
    overlay.querySelector('.aria2-close').addEventListener('click', close);
    overlay.querySelector('.aria2-save').addEventListener('click', onSave);
    overlay.querySelector('.aria2-test').addEventListener('click', onTest);
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && overlay && !overlay.classList.contains('hidden')) close();
    });
    return overlay;
  }

  function modalEls() {
    buildModal();
    return {
      rpcUrl: overlay.querySelector('#aria2RpcUrl'),
      token: overlay.querySelector('#aria2Token'),
      dir: overlay.querySelector('#aria2Dir'),
      error: overlay.querySelector('.aria2-error'),
      test: overlay.querySelector('.aria2-test'),
    };
  }

  function readModal() {
    const els = modalEls();
    return { rpcUrl: els.rpcUrl.value, token: els.token.value, dir: els.dir.value };
  }

  function showModalError(msg) {
    const els = modalEls();
    els.error.textContent = msg;
    els.error.classList.toggle('hidden', !msg);
  }

  function openConfig() {
    const cfg = loadConfig();
    const els = modalEls();
    els.rpcUrl.value = cfg.rpcUrl || '';
    els.token.value = cfg.token || '';
    els.dir.value = cfg.dir || '';
    showModalError('');
    overlay.classList.remove('closing');
    overlay.classList.remove('hidden');
    requestAnimationFrame(() => els.rpcUrl.focus());
  }

  // hideModal plays the exit animation, then sets display:none once it ends.
  // The animationend listener is one-shot and a timeout backstops it in case
  // the animation is suppressed (e.g. prefers-reduced-motion, tab hidden).
  function hideModal() {
    if (!overlay || overlay.classList.contains('hidden') || overlay.classList.contains('closing')) {
      return;
    }
    overlay.classList.add('closing');
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      overlay.classList.add('hidden');
      overlay.classList.remove('closing');
    };
    overlay.addEventListener('animationend', function handler(e) {
      if (e.target !== overlay) return;
      overlay.removeEventListener('animationend', handler);
      finish();
    });
    window.setTimeout(finish, 320);
  }

  function onSave() {
    const cfg = readModal();
    if (!String(cfg.rpcUrl || '').trim()) {
      showModalError('请填写 RPC 地址');
      return;
    }
    saveConfig(cfg);
    hideModal();
    toast('aria2 配置已保存', 'success');
  }

  async function onTest() {
    const cfg = saveConfig(readModal());
    if (!cfg.rpcUrl) {
      showModalError('请填写 RPC 地址');
      return;
    }
    const els = modalEls();
    showModalError('');
    els.test.disabled = true;
    const original = els.test.textContent;
    els.test.textContent = '测试中...';
    try {
      const version = await rpc('aria2.getVersion', [], cfg);
      toast('连接成功：aria2 ' + (version?.version || ''), 'success');
    } catch (e) {
      showModalError(e.message);
    } finally {
      els.test.disabled = false;
      els.test.textContent = original;
    }
  }

  // --- Buttons the host pages mount ---

  function configButton() {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'secondary compact aria2-config-btn';
    btn.innerHTML = ICON_GEAR + '<span class="button-label">aria2 配置</span>';
    btn.addEventListener('click', openConfig);
    return btn;
  }

  // pushButton builds a compact "推送" button. getUrl/getName are called at
  // click time so the button always reads the latest resolved value.
  function pushButton(getUrl, getName, label) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'secondary compact aria2-push-btn';
    btn.innerHTML = ICON_PUSH + '<span class="button-label">' + (label || '推送') + '</span>';
    btn.addEventListener('click', () => {
      const url = typeof getUrl === 'function' ? getUrl() : getUrl;
      const name = typeof getName === 'function' ? getName() : getName;
      pushOne(url, name);
    });
    return btn;
  }

  // --- Toast (self-contained; reuses the shared .toast styles) ---

  function toast(message, level) {
    const el = document.createElement('div');
    el.className = 'toast toast-' + (level || 'info');
    el.textContent = message;
    document.body.appendChild(el);
    window.setTimeout(() => el.classList.add('show'), 10);
    window.setTimeout(() => {
      el.classList.remove('show');
      window.setTimeout(() => el.remove(), 300);
    }, 3000);
  }

  window.Aria2 = {
    loadConfig,
    saveConfig,
    isConfigured,
    openConfig,
    addUri,
    pushOne,
    pushMany,
    configButton,
    pushButton,
  };
})();
