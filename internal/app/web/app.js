const navButtons = Array.from(document.querySelectorAll('.nav-button'));
const resolvePage = document.getElementById('resolvePage');
const accountsPage = document.getElementById('accountsPage');
const logsPage = document.getElementById('logsPage');

const metricAccountCount = document.getElementById('metricAccountCount');
const metricAvailableCount = document.getElementById('metricAvailableCount');
const metricFailedCount = document.getElementById('metricFailedCount');
const metricRootFolder = document.getElementById('metricRootFolder');

const resolveForm = document.getElementById('resolveForm');
const resourceInput = document.getElementById('resourceInput');
const passCodeInput = document.getElementById('passCode');
const modeInputs = Array.from(document.querySelectorAll('input[name="mode"]'));
const submitButton = document.getElementById('submitButton');
const resolveHint = document.getElementById('resolveHint');

const jobBadge = document.getElementById('jobBadge');
const jobMessage = document.getElementById('jobMessage');
const jobError = document.getElementById('jobError');
const attemptList = document.getElementById('attemptList');
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

const accountForm = document.getElementById('accountForm');
const accountUsername = document.getElementById('accountUsername');
const accountPassword = document.getElementById('accountPassword');
const accountSubmitButton = document.getElementById('accountSubmitButton');
const accountFormError = document.getElementById('accountFormError');
const accountList = document.getElementById('accountList');
const clearLogsButton = document.getElementById('clearLogsButton');
const logList = document.getElementById('logList');

const state = {
  config: null,
  accounts: [],
  logs: [],
  lastLogId: 0,
  currentPage: 'resolve',
  resolveBusy: false,
  accountBusy: false,
};

let currentJobId = null;
let pollTimer = null;
let logPollTimer = null;

boot();

async function boot() {
  bindActions();
  await refreshAppState();
  showPage('resolve');
  startLogPolling();
}

function bindActions() {
  navButtons.forEach((button) => {
    button.addEventListener('click', () => showPage(button.dataset.page));
  });
  resolveForm.addEventListener('submit', onResolveSubmit);
  accountForm.addEventListener('submit', onAccountSubmit);
  clearLogsButton.addEventListener('click', clearLogs);
  directCopy.addEventListener('click', () => copyText(directValue.value, directCopy));
  proxyCopy.addEventListener('click', () => copyText(proxyValue.value, proxyCopy));
}

function showPage(page) {
  state.currentPage = page;
  resolvePage.classList.toggle('active', page === 'resolve');
  accountsPage.classList.toggle('active', page === 'accounts');
  logsPage.classList.toggle('active', page === 'logs');
  navButtons.forEach((button) => {
    button.classList.toggle('active', button.dataset.page === page);
  });
  if (page === 'logs') {
    renderLogs();
  }
}

async function refreshAppState() {
  try {
    const [config, accountPayload] = await Promise.all([
      api('/api/config'),
      api('/api/accounts'),
    ]);
    state.config = config;
    state.accounts = accountPayload.accounts || [];
    renderMetrics();
    renderAccounts();
    renderAvailability();
  } catch (error) {
    showJobError(error.message);
  }
}

function renderMetrics() {
  const total = state.accounts.length;
  const failed = state.accounts.filter((account) => account.status === 'failed').length;
  const available = total - failed;

  metricAccountCount.textContent = String(total);
  metricAvailableCount.textContent = String(available);
  metricFailedCount.textContent = String(failed);
  metricRootFolder.textContent = state.config?.root_folder || '-';

  if (total === 0) {
    resolveHint.textContent = '请先添加账号';
    resolveHint.className = 'status-pill warn';
  } else if (failed === total) {
    resolveHint.textContent = '会继续尝试重登';
    resolveHint.className = 'status-pill warn';
  } else {
    resolveHint.textContent = `${total} 个账号`;
    resolveHint.className = 'status-pill success';
  }
}

function renderAccounts() {
  accountList.innerHTML = '';

  if (state.accounts.length === 0) {
    accountList.className = 'account-list empty';
    accountList.textContent = '还没有账号';
    return;
  }

  accountList.className = 'account-list';
  for (const account of state.accounts) {
    const card = document.createElement('article');
    card.className = 'account-card';

    const main = document.createElement('div');
    main.className = 'account-main';

    const heading = document.createElement('div');
    heading.className = 'account-heading';

    const title = document.createElement('strong');
    title.textContent = account.username;
    heading.appendChild(title);

    heading.appendChild(createPremiumBadge(account));

    const premiumUntil = formatPremiumUntil(account.premium_until);
    if (premiumUntil) {
      const expireBadge = document.createElement('span');
      expireBadge.className = 'status-pill neutral compact-pill';
      expireBadge.textContent = `到期 ${premiumUntil}`;
      heading.appendChild(expireBadge);
    }

    main.appendChild(heading);

    const meta = document.createElement('div');
    meta.className = 'muted';
    meta.textContent = account.logged_in ? 'session 已保存' : '将使用账号密码登录';
    main.appendChild(meta);

    if (account.last_error) {
      const error = document.createElement('p');
      error.className = 'account-error';
      error.textContent = account.last_error;
      main.appendChild(error);
    }

    const side = document.createElement('div');
    side.className = 'account-side';

    const badge = document.createElement('span');
    badge.className = `status-pill ${account.status === 'failed' ? 'danger' : 'success'}`;
    badge.textContent = account.status === 'failed' ? '失败' : '可用';
    side.appendChild(badge);

    const actions = document.createElement('div');
    actions.className = 'mini-actions';

    const resetButton = document.createElement('button');
    resetButton.type = 'button';
    resetButton.className = 'secondary compact';
    resetButton.textContent = '重置';
    resetButton.disabled = account.status !== 'failed';
    resetButton.addEventListener('click', () => resetAccount(account.id));
    actions.appendChild(resetButton);

    const deleteButton = document.createElement('button');
    deleteButton.type = 'button';
    deleteButton.className = 'danger-button compact';
    deleteButton.textContent = '删除';
    deleteButton.addEventListener('click', () => deleteAccount(account.id));
    actions.appendChild(deleteButton);

    side.appendChild(actions);
    card.appendChild(main);
    card.appendChild(side);
    accountList.appendChild(card);
  }
}

function createPremiumBadge(account) {
  const badge = document.createElement('span');
  badge.className = 'status-pill neutral compact-pill';

  if (account.premium) {
    badge.className = 'status-pill success compact-pill';
    badge.textContent = account.premium_type ? `会员 ${account.premium_type}` : '会员';
    return badge;
  }

  if (account.premium_error) {
    badge.className = 'status-pill warn compact-pill';
    badge.textContent = '会员未知';
    badge.title = account.premium_error;
    return badge;
  }

  badge.textContent = '非会员';
  return badge;
}

function renderAvailability() {
  const hasAccounts = state.accounts.length > 0;
  const canResolve = hasAccounts && !state.resolveBusy;

  resourceInput.disabled = !hasAccounts;
  passCodeInput.disabled = !hasAccounts;
  modeInputs.forEach((input) => {
    input.disabled = !hasAccounts;
  });
  submitButton.disabled = !canResolve;
  submitButton.textContent = state.resolveBusy ? '处理中...' : '开始解析';

  accountSubmitButton.disabled = state.accountBusy;
  accountSubmitButton.textContent = state.accountBusy ? '登录中...' : '添加并登录';
}

async function onAccountSubmit(event) {
  event.preventDefault();
  setAccountBusy(true);
  hideAccountError();

  try {
    await api('/api/accounts', {
      method: 'POST',
      body: JSON.stringify({
        username: accountUsername.value.trim(),
        password: accountPassword.value,
      }),
    });
    accountPassword.value = '';
    await refreshAppState();
  } catch (error) {
    showAccountError(error.message);
  } finally {
    setAccountBusy(false);
  }
}

async function deleteAccount(id) {
  try {
    await api(`/api/accounts/${id}`, { method: 'DELETE' });
    await refreshAppState();
  } catch (error) {
    showAccountError(error.message);
  }
}

async function resetAccount(id) {
  try {
    await api(`/api/accounts/${id}/reset`, { method: 'POST' });
    await refreshAppState();
  } catch (error) {
    showAccountError(error.message);
  }
}

async function onResolveSubmit(event) {
  event.preventDefault();
  if (state.accounts.length === 0) {
    showPage('accounts');
    showAccountError('请先添加至少一个 PikPak 账号。');
    return;
  }

  clearJobUI();
  setResolveBusy(true);

  try {
    const job = await api('/api/jobs', {
      method: 'POST',
      body: JSON.stringify({
        input: resourceInput.value.trim(),
        pass_code: passCodeInput.value.trim(),
        mode: resolveForm.elements.mode.value,
      }),
    });

    currentJobId = job.id;
    renderJob(job);
    startPolling();
  } catch (error) {
    showJobError(error.message);
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
    const job = await api(`/api/jobs/${currentJobId}`);
    renderJob(job);
    if (['completed', 'failed', 'selection_required'].includes(job.status)) {
      setResolveBusy(false);
      await refreshAppState();
      stopPolling();
    }
  } catch (error) {
    stopPolling();
    setResolveBusy(false);
    showJobError(error.message);
  }
}

function renderJob(job) {
  jobBadge.textContent = humanizeStatus(job.status);
  jobBadge.className = `status-pill ${badgeClass(job.status)}`;
  jobMessage.textContent = job.message || '处理中...';
  jobMessage.classList.remove('hidden');
  renderAttempts(job.account_attempts || []);

  if (job.error) {
    showJobError(job.error);
  } else {
    hideJobError();
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

function renderAttempts(attempts) {
  attemptList.innerHTML = '';
  if (attempts.length === 0) {
    attemptList.className = 'attempt-list empty';
    attemptList.textContent = '暂无尝试记录';
    return;
  }

  attemptList.className = 'attempt-list';
  for (const attempt of attempts) {
    const row = document.createElement('div');
    row.className = 'attempt-item';

    const name = document.createElement('strong');
    name.textContent = attempt.username || attempt.account_id;
    row.appendChild(name);

    const badge = document.createElement('span');
    badge.className = `status-pill ${attemptClass(attempt.status)}`;
    badge.textContent = humanizeAttempt(attempt.status);
    row.appendChild(badge);

    if (attempt.error) {
      const error = document.createElement('p');
      error.textContent = attempt.error;
      row.appendChild(error);
    }

    attemptList.appendChild(row);
  }
}

function renderSelection(job) {
  selectionPanel.classList.remove('hidden');
  selectionHint.textContent = job.stage === 'source_selection'
    ? '先选择要转存的项目'
    : '选择最终生成链接的文件';
  selectionList.innerHTML = '';

  for (const item of job.items || []) {
    const row = document.createElement('article');
    row.className = 'selection-item';

    const info = document.createElement('div');
    info.className = 'selection-info';

    const title = document.createElement('strong');
    title.textContent = item.name;
    info.appendChild(title);

    const meta = document.createElement('div');
    meta.className = 'muted';
    const kind = item.kind && item.kind.toLowerCase().includes('folder') ? '文件夹' : '文件';
    const size = formatBytes(item.size);
    meta.textContent = [item.path || item.name, kind, size].filter(Boolean).join(' · ');
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
  const previousText = button.textContent;
  button.disabled = true;
  button.textContent = '处理中...';

  try {
    const job = await api(`/api/jobs/${jobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_id: itemId }),
    });
    renderJob(job);
    currentJobId = job.id;
    startPolling();
  } catch (error) {
    showJobError(error.message);
  } finally {
    button.disabled = false;
    button.textContent = previousText;
  }
}

function renderResult(job) {
  const result = job.result;
  resultPanel.classList.remove('hidden');
  resultMode.textContent = job.mode === 'proxy' ? '优先代理' : '优先直链';
  resultMode.className = `status-pill ${job.mode === 'proxy' ? 'warn' : 'success'}`;
  resultName.textContent = result.file.name || '-';
  resultPath.textContent = result.file.path || '-';
  resultSize.textContent = formatBytes(result.file.size);
  resultExpire.textContent = formatDateTime(result.expires_at);
  directValue.value = result.direct_url || '';
  proxyValue.value = result.proxy_url || '';
  directOpen.href = result.direct_url || '#';
  proxyOpen.href = result.proxy_url || '#';
}

function startLogPolling() {
  stopLogPolling();
  fetchLogs();
  logPollTimer = window.setInterval(fetchLogs, 1600);
}

function stopLogPolling() {
  if (logPollTimer !== null) {
    window.clearInterval(logPollTimer);
    logPollTimer = null;
  }
}

async function fetchLogs() {
  try {
    const payload = await api(`/api/logs?after=${state.lastLogId}`);
    const nextLogs = payload.logs || [];
    if (nextLogs.length === 0) {
      renderLogs();
      return;
    }

    for (const entry of nextLogs) {
      state.logs.push(entry);
      state.lastLogId = Math.max(state.lastLogId, Number(entry.id) || 0);
    }
    if (state.logs.length > 500) {
      state.logs = state.logs.slice(-500);
    }
    renderLogs();
  } catch {
    stopLogPolling();
  }
}

function renderLogs() {
  logList.innerHTML = '';

  if (state.logs.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'console-empty';
    empty.textContent = '暂无日志';
    logList.appendChild(empty);
    return;
  }

  for (const entry of state.logs) {
    const row = document.createElement('article');
    row.className = `console-entry ${entry.level || 'info'}`;

    const glyph = document.createElement('span');
    glyph.className = 'console-glyph';
    glyph.textContent = logGlyph(entry.level);
    row.appendChild(glyph);

    const body = document.createElement('div');
    body.className = 'console-entry-body';

    const line = document.createElement('div');
    line.className = 'console-line';

    if (entry.job_id) {
      const job = document.createElement('span');
      job.className = 'console-job';
      job.textContent = `[${entry.job_id}]`;
      line.appendChild(job);
    }

    const message = document.createElement('span');
    message.className = 'console-message';
    message.textContent = entry.message || '-';
    line.appendChild(message);
    body.appendChild(line);

    if (entry.details?.length) {
      const details = document.createElement('div');
      details.className = 'console-details';
      for (const detailText of entry.details) {
        const detail = document.createElement('span');
        detail.textContent = detailText;
        details.appendChild(detail);
      }
      body.appendChild(details);
    }

    const time = document.createElement('time');
    time.className = 'console-time';
    time.dateTime = entry.time || '';
    time.textContent = formatLogTime(entry.time);

    row.appendChild(body);
    row.appendChild(time);
    logList.appendChild(row);
  }

  if (state.currentPage === 'logs') {
    logList.scrollTop = logList.scrollHeight;
  }
}

async function clearLogs() {
  clearLogsButton.disabled = true;
  try {
    await api('/api/logs', { method: 'DELETE' });
    state.logs = [];
    state.lastLogId = 0;
    renderLogs();
  } finally {
    clearLogsButton.disabled = false;
  }
}

function clearJobUI() {
  stopPolling();
  currentJobId = null;
  hideJobError();
  selectionPanel.classList.add('hidden');
  selectionList.innerHTML = '';
  resultPanel.classList.add('hidden');
  renderAttempts([]);
  jobBadge.textContent = '空闲';
  jobBadge.className = 'status-pill neutral';
  jobMessage.textContent = '';
  jobMessage.classList.add('hidden');
}

function setResolveBusy(busy) {
  state.resolveBusy = busy;
  renderAvailability();
}

function setAccountBusy(busy) {
  state.accountBusy = busy;
  renderAvailability();
}

function showAccountError(message) {
  accountFormError.textContent = message;
  accountFormError.classList.remove('hidden');
}

function hideAccountError() {
  accountFormError.textContent = '';
  accountFormError.classList.add('hidden');
}

function showJobError(message) {
  jobError.textContent = message;
  jobError.classList.remove('hidden');
}

function hideJobError() {
  jobError.textContent = '';
  jobError.classList.add('hidden');
}

async function copyText(value, button) {
  if (!value) {
    return;
  }
  const original = button.textContent;
  try {
    await navigator.clipboard.writeText(value);
    button.textContent = '已复制';
  } catch {
    button.textContent = '复制失败';
  }
  window.setTimeout(() => {
    button.textContent = original;
  }, 1200);
}

function humanizeStatus(status) {
  switch (status) {
    case 'queued':
      return '排队中';
    case 'running':
      return '处理中';
    case 'selection_required':
      return '等待选择';
    case 'completed':
      return '已完成';
    case 'failed':
      return '失败';
    default:
      return status || '未知';
  }
}

function badgeClass(status) {
  switch (status) {
    case 'completed':
      return 'success';
    case 'failed':
      return 'danger';
    case 'selection_required':
      return 'warn';
    case 'running':
      return 'neutral';
    default:
      return 'neutral';
  }
}

function humanizeAttempt(status) {
  switch (status) {
    case 'running':
      return '尝试中';
    case 'success':
      return '成功';
    case 'failed':
      return '失败';
    default:
      return status || '未知';
  }
}

function attemptClass(status) {
  switch (status) {
    case 'success':
      return 'success';
    case 'failed':
      return 'danger';
    case 'running':
      return 'warn';
    default:
      return 'neutral';
  }
}

function formatBytes(raw) {
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0) {
    return '-';
  }
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
  if (!value) {
    return '-';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

function formatLogTime(value) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleTimeString('zh-CN', { hour12: false });
}

function formatPremiumUntil(value) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleDateString();
}

function logGlyph(level) {
  switch (level) {
    case 'success':
      return 'ok';
    case 'warn':
      return '!';
    case 'error':
      return 'x';
    default:
      return 'i';
  }
}

async function api(endpoint, options = {}) {
  const response = await fetch(endpoint, {
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
    ...options,
  });

  const isJSON = response.headers.get('content-type')?.includes('application/json');
  const payload = isJSON ? await response.json() : null;

  if (!response.ok) {
    throw new Error(payload?.error || `请求失败 (${response.status})`);
  }

  return payload;
}
