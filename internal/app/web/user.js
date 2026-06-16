const gateView = document.getElementById('gateView');
const appView = document.getElementById('appView');
const cdkForm = document.getElementById('cdkForm');
const cdkInput = document.getElementById('cdkInput');
const cdkSubmit = document.getElementById('cdkSubmit');
const cdkError = document.getElementById('cdkError');

const cdkRemainingPill = document.getElementById('cdkRemainingPill');
const cdkDaysPill = document.getElementById('cdkDaysPill');
const queuePill = document.getElementById('queuePill');
const logoutButton = document.getElementById('logoutButton');

const resolveForm = document.getElementById('resolveForm');
const resourceInput = document.getElementById('resourceInput');
const passCodeInput = document.getElementById('passCode');
const submitButton = document.getElementById('submitButton');
const jobBadge = document.getElementById('jobBadge');
const jobMessage = document.getElementById('jobMessage');
const jobError = document.getElementById('jobError');
const selectionPanel = document.getElementById('selectionPanel');
const selectionHint = document.getElementById('selectionHint');
const selectionList = document.getElementById('selectionList');
const resultPanel = document.getElementById('resultPanel');
const resultMode = document.getElementById('resultMode');
const resultName = document.getElementById('resultName');
const resultPath = document.getElementById('resultPath');
const resultSize = document.getElementById('resultSize');
const resultExpire = document.getElementById('resultExpire');
const directOpen = document.getElementById('directOpen');
const directCopy = document.getElementById('directCopy');
const directValue = document.getElementById('directValue');
const proxyOpen = document.getElementById('proxyOpen');
const proxyCopy = document.getElementById('proxyCopy');
const proxyValue = document.getElementById('proxyValue');

let currentJobId = null;
let pollTimer = null;
let statusTimer = null;
let resolveBusy = false;

boot();

async function boot() {
  cdkForm.addEventListener('submit', onCdkSubmit);
  logoutButton.addEventListener('click', onLogout);
  resolveForm.addEventListener('submit', onResolveSubmit);
  directCopy.addEventListener('click', () => copyText(directValue.value, directCopy));
  proxyCopy.addEventListener('click', () => copyText(proxyValue.value, proxyCopy));

  const presetCode = new URLSearchParams(location.search).get('code');
  if (presetCode) {
    cdkInput.value = presetCode.trim().toUpperCase();
  }

  try {
    const status = await api('/api/u/status');
    enterApp(status);
    return;
  } catch {
    showGate();
  }

  // Auto-submit when arriving via a share link that carries a code.
  if (presetCode) {
    cdkForm.requestSubmit();
  }
}

function showGate() {
  stopStatusPolling();
  appView.classList.add('hidden');
  gateView.classList.remove('hidden');
  cdkInput.focus();
}

function enterApp(status) {
  gateView.classList.add('hidden');
  appView.classList.remove('hidden');
  renderStatus(status);
  startStatusPolling();
}

function renderStatus(status) {
  if (!status) return;
  cdkRemainingPill.textContent = `剩余 ${status.remaining} 次`;
  cdkRemainingPill.className = `status-pill ${status.remaining > 0 ? 'success' : 'danger'}`;
  cdkDaysPill.textContent = status.expired ? '已过期' : `到期 ${status.days_left} 天`;
  cdkDaysPill.className = `status-pill ${status.expired ? 'danger' : 'neutral'}`;
  renderQueue(status.queue);

  const usable = status.remaining > 0 && !status.expired;
  resourceInput.disabled = !usable;
  passCodeInput.disabled = !usable;
  submitButton.disabled = !usable || resolveBusy;
  setButtonLabel(submitButton, resolveBusy ? '处理中...' : usable ? '开始解析' : '次数已用完或已过期');
}

// renderQueue reflects the global resolution queue on the header pill so a user
// can see how busy the system is before and after submitting.
function renderQueue(queue) {
  if (!queuePill) return;
  if (!queue) {
    queuePill.classList.add('hidden');
    return;
  }
  const waiting = Number(queue.waiting) || 0;
  const active = Boolean(queue.active);
  queuePill.classList.remove('hidden', 'neutral', 'warn', 'is-running');
  if (waiting > 0) {
    queuePill.textContent = `排队 ${waiting} 条`;
    queuePill.classList.add('warn', 'is-running');
  } else if (active) {
    queuePill.textContent = '解析中';
    queuePill.classList.add('neutral', 'is-running');
  } else {
    queuePill.textContent = '队列空闲';
    queuePill.classList.add('neutral');
  }
}

function startStatusPolling() {
  stopStatusPolling();
  statusTimer = window.setInterval(refreshStatus, 5000);
}

function stopStatusPolling() {
  if (statusTimer !== null) {
    window.clearInterval(statusTimer);
    statusTimer = null;
  }
}

async function refreshStatus() {
  try {
    renderStatus(await api('/api/u/status'));
  } catch {
    showGate();
  }
}

async function onCdkSubmit(event) {
  event.preventDefault();
  hide(cdkError);
  cdkSubmit.disabled = true;
  setButtonLabel(cdkSubmit, '验证中...');
  try {
    const status = await api('/api/u/login', {
      method: 'POST',
      body: JSON.stringify({ code: cdkInput.value.trim() }),
    });
    enterApp(status);
  } catch (error) {
    cdkError.textContent = error.message;
    cdkError.classList.remove('hidden');
  } finally {
    cdkSubmit.disabled = false;
    setButtonLabel(cdkSubmit, '进入');
  }
}

async function onLogout() {
  try {
    await api('/api/u/logout', { method: 'POST' });
  } catch { /* ignore */ }
  stopPolling();
  cdkInput.value = '';
  clearJobUI();
  showGate();
}

async function onResolveSubmit(event) {
  event.preventDefault();
  clearJobUI();
  setResolveBusy(true);
  try {
    const job = await api('/api/u/jobs', {
      method: 'POST',
      body: JSON.stringify({
        input: resourceInput.value.trim(),
        pass_code: passCodeInput.value.trim(),
        mode: resolveForm.elements.mode.value,
      }),
    });
    currentJobId = job.id;
    renderJob(job);
    refreshStatus();
    startPolling();
  } catch (error) {
    showError(jobError, error.message);
    setResolveBusy(false);
  }
}

function startPolling() {
  stopPolling();
  pollTimer = window.setInterval(fetchJob, 2000);
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
}

async function fetchJob() {
  if (!currentJobId) {
    stopPolling();
    return;
  }
  try {
    const job = await api(`/api/u/jobs/${currentJobId}`);
    renderJob(job);
    if (['completed', 'failed', 'selection_required'].includes(job.status)) {
      setResolveBusy(false);
      stopPolling();
      refreshStatus();
    }
  } catch (error) {
    stopPolling();
    setResolveBusy(false);
    showError(jobError, error.message);
  }
}

function renderJob(job) {
  jobBadge.textContent = humanizeStatus(job.status);
  jobBadge.className = `status-pill ${badgeClass(job.status)}`;
  if (job.status === 'running' || job.status === 'queued') {
    jobBadge.classList.add('is-running');
  }

  if (job.status === 'queued') {
    const ahead = Number(job.queue_ahead) || 0;
    jobMessage.textContent = ahead > 0
      ? `排队中：前方还有 ${ahead} 条链接`
      : '排队中：等待当前任务完成…';
  } else {
    jobMessage.textContent = job.message || '处理中...';
  }
  jobMessage.classList.remove('hidden');

  if (job.error) {
    showError(jobError, job.error);
  } else {
    hide(jobError);
  }

  if (job.status === 'selection_required') {
    renderSelection(job);
  } else {
    selectionPanel.classList.add('hidden');
    selectionList.innerHTML = '';
  }

  if (job.result) {
    renderResult(job);
  } else {
    resultPanel.classList.add('hidden');
  }
}

function renderSelection(job) {
  selectionPanel.classList.remove('hidden');
  selectionHint.textContent = job.stage === 'source_selection' ? '先选择要转存的项目' : '选择最终生成链接的文件';
  selectionList.innerHTML = '';

  for (const item of job.items || []) {
    const row = document.createElement('article');
    row.className = 'selection-item neutral';

    const info = document.createElement('div');
    info.className = 'selection-info';
    const title = document.createElement('strong');
    title.textContent = item.name || '未命名项目';
    info.appendChild(title);
    const meta = document.createElement('div');
    meta.className = 'muted';
    const kind = item.kind && item.kind.toLowerCase().includes('folder') ? '文件夹' : '文件';
    meta.textContent = [item.path || item.name, kind, formatBytes(item.size)].filter(Boolean).join(' · ');
    info.appendChild(meta);

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'secondary';
    button.textContent = job.stage === 'source_selection' ? '转存这一项' : '生成链接';
    button.addEventListener('click', () => chooseItem(job.id, item.id, button));

    row.appendChild(info);
    row.appendChild(button);
    selectionList.appendChild(row);
  }
}

async function chooseItem(jobId, itemId, button) {
  button.disabled = true;
  const original = button.textContent;
  button.textContent = '处理中...';
  try {
    const job = await api(`/api/u/jobs/${jobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_id: itemId }),
    });
    currentJobId = job.id;
    renderJob(job);
    startPolling();
  } catch (error) {
    showError(jobError, error.message);
  } finally {
    button.disabled = false;
    button.textContent = original;
  }
}

function renderResult(job) {
  const result = job.result;
  resultPanel.classList.remove('hidden');
  resultMode.textContent = job.mode === 'proxy' ? '代理优先' : '直链优先';
  resultMode.className = `status-pill ${job.mode === 'proxy' ? 'warn' : 'success'}`;
  resultName.textContent = result.file?.name || '-';
  resultPath.textContent = result.file?.path || '-';
  resultSize.textContent = formatBytes(result.file?.size);
  resultExpire.textContent = formatDateTime(result.expires_at);

  directValue.value = result.direct_url || '';
  proxyValue.value = result.proxy_url || '';
  directOpen.href = result.direct_url || '#';
  proxyOpen.href = result.proxy_url || '#';
  directCopy.disabled = !result.direct_url;
  proxyCopy.disabled = !result.proxy_url;

  requestAnimationFrame(() => resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
}

function clearJobUI() {
  stopPolling();
  currentJobId = null;
  hide(jobError);
  selectionPanel.classList.add('hidden');
  selectionList.innerHTML = '';
  resultPanel.classList.add('hidden');
  jobBadge.textContent = '空闲';
  jobBadge.className = 'status-pill neutral';
  jobMessage.textContent = '';
  jobMessage.classList.add('hidden');
}

function setResolveBusy(busy) {
  resolveBusy = busy;
  refreshStatus();
}

async function copyText(value, button) {
  if (!value) return;
  const original = getButtonLabel(button);
  try {
    await navigator.clipboard.writeText(value);
    setButtonLabel(button, '已复制');
  } catch {
    setButtonLabel(button, '复制失败');
  }
  window.setTimeout(() => setButtonLabel(button, original), 1200);
}

function humanizeStatus(status) {
  switch (status) {
    case 'queued': return '排队中';
    case 'running': return '处理中';
    case 'selection_required': return '等待选择';
    case 'completed': return '已完成';
    case 'failed': return '失败';
    default: return status || '未知';
  }
}

function badgeClass(status) {
  switch (status) {
    case 'completed': return 'success';
    case 'failed': return 'danger';
    case 'selection_required': return 'warn';
    default: return 'neutral';
  }
}

function formatBytes(raw) {
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0) return '-';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size.toFixed(size >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatDateTime(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', { hour12: false });
}

function setButtonLabel(button, label) {
  const node = button.querySelector('.button-label');
  if (node) node.textContent = label;
  else button.textContent = label;
}

function getButtonLabel(button) {
  return button.querySelector('.button-label')?.textContent || button.textContent.trim();
}

function showError(el, message) {
  el.textContent = message;
  el.classList.remove('hidden');
}

function hide(el) {
  el.textContent = '';
  el.classList.add('hidden');
}

async function api(endpoint, options = {}) {
  const response = await fetch(endpoint, {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const isJSON = response.headers.get('content-type')?.includes('application/json');
  const payload = isJSON ? await response.json() : null;
  if (!response.ok) {
    throw new Error(payload?.error || `请求失败 (${response.status})`);
  }
  return payload;
}
