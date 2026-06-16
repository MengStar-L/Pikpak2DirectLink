const navButtons = Array.from(document.querySelectorAll('.nav-button[data-page]'));
const resolvePage = document.getElementById('resolvePage');
const accountsPage = document.getElementById('accountsPage');
const logsPage = document.getElementById('logsPage');

const logoutButton = document.getElementById('logoutButton');
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
const linkTypeIndicator = document.getElementById('linkTypeIndicator');

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

const passwordForm = document.getElementById('passwordForm');
const currentPassword = document.getElementById('currentPassword');
const newPassword = document.getElementById('newPassword');
const confirmPassword = document.getElementById('confirmPassword');
const passwordSubmitButton = document.getElementById('passwordSubmitButton');
const passwordFormError = document.getElementById('passwordFormError');
const passwordFixedNote = document.getElementById('passwordFixedNote');

const clearLogsButton = document.getElementById('clearLogsButton');
const logList = document.getElementById('logList');

const updatePage = document.getElementById('updatePage');
const settingsPage = document.getElementById('settingsPage');
const updateNavDot = document.getElementById('updateNavDot');
const updateStatusPill = document.getElementById('updateStatusPill');
const updateCurrentVersion = document.getElementById('updateCurrentVersion');
const updateLatestVersion = document.getElementById('updateLatestVersion');
const updatePlatform = document.getElementById('updatePlatform');
const updateCheckedAt = document.getElementById('updateCheckedAt');
const updateProgress = document.getElementById('updateProgress');
const updateProgressLabel = document.getElementById('updateProgressLabel');
const updateProgressPercent = document.getElementById('updateProgressPercent');
const updateProgressFill = document.getElementById('updateProgressFill');
const updateProgressMeta = document.getElementById('updateProgressMeta');
const updateError = document.getElementById('updateError');
const checkUpdateButton = document.getElementById('checkUpdateButton');
const installUpdateButton = document.getElementById('installUpdateButton');
const updateReleaseLink = document.getElementById('updateReleaseLink');
const updateNotes = document.getElementById('updateNotes');
const updateNotesBody = document.getElementById('updateNotesBody');

const state = {
  config: null,
  accounts: [],
  logs: [],
  lastLogId: 0,
  currentPage: 'resolve',
  resolveBusy: false,
  accountBusy: false,
  authenticated: false,
  update: null,
};

let currentJobId = null;
let pollTimer = null;
let logPollTimer = null;
let updatePollTimer = null;
let updateStatusTimer = null;
let updateRestartPending = false;
let updateServerWentDown = false;
let updateRestartDeadline = 0;

const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

boot();

async function boot() {
  bindActions();
  clearJobUI();
  await refreshAppState();
  showPage('resolve');
  if (state.authenticated) {
    startUpdateStatusPolling();
  }
}

function bindActions() {
  navButtons.forEach((button) => {
    button.addEventListener('click', () => showPage(button.dataset.page));
  });
  document.addEventListener('pointerdown', spawnRipple);
  resolveForm.addEventListener('submit', onResolveSubmit);
  accountForm.addEventListener('submit', onAccountSubmit);
  passwordForm.addEventListener('submit', onChangePassword);
  logoutButton?.addEventListener('click', onLogout);
  clearLogsButton.addEventListener('click', clearLogs);
  checkUpdateButton.addEventListener('click', onCheckUpdate);
  installUpdateButton.addEventListener('click', onInstallUpdate);
  directCopy.addEventListener('click', () => copyText(directValue.value, directCopy));
  proxyCopy.addEventListener('click', () => copyText(proxyValue.value, proxyCopy));
  resourceInput.addEventListener('input', detectLinkType);
}

function showPage(page) {
  const pages = {
    resolve: resolvePage,
    accounts: accountsPage,
    logs: logsPage,
    update: updatePage,
    settings: settingsPage,
  };
  if (!pages[page]) return;

  state.currentPage = page;
  Object.entries(pages).forEach(([name, element]) => {
    element.classList.toggle('active', name === page);
  });
  navButtons.forEach((button) => {
    button.classList.toggle('active', button.dataset.page === page);
  });
  if (page === 'logs') {
    renderLogs();
  }
  if (page === 'update') {
    fetchUpdateStatus();
  }
}

async function refreshAppState() {
  try {
    const config = await api('/api/config');
    state.config = config;
    state.authenticated = Boolean(config.authenticated);
    renderAuthUI();

    if (config.auth_required && !config.authenticated) {
      redirectToGate();
      return;
    }

    const accountPayload = await api('/api/accounts');
    state.accounts = accountPayload.accounts || [];
    renderMetrics();
    renderAccounts();
    renderPasswordPanel();
    renderAvailability();
    renderAuthUI();
    ensureLogPolling();
  } catch (error) {
    if (error.message.includes('authentication required')) {
      redirectToGate();
      return;
    }
    showToast(error.message, 'error');
  }
}

function renderMetrics() {
  const total = state.accounts.length;
  const failed = state.accounts.filter((account) => account.status === 'failed').length;
  const available = Math.max(0, total - failed);

  animateCount(metricAccountCount, total);
  animateCount(metricAvailableCount, available);
  animateCount(metricFailedCount, failed);
  metricRootFolder.textContent = state.config?.root_folder || '-';

  if (total === 0) {
    resolveHint.textContent = '请先添加账号';
    resolveHint.className = 'status-pill warn';
  } else if (failed === total) {
    resolveHint.textContent = '账号需检查';
    resolveHint.className = 'status-pill warn';
  } else {
    resolveHint.textContent = `${available}/${total} 个可用`;
    resolveHint.className = 'status-pill success';
  }
}

function renderAccounts() {
  accountList.innerHTML = '';

  if (state.accounts.length === 0) {
    accountList.className = 'account-list empty';
    accountList.textContent = '还没有账号，先添加一个 PikPak 账号';
    return;
  }

  accountList.className = 'account-list';
  for (const account of state.accounts) {
    const card = document.createElement('article');
    card.className = `account-card ${account.status === 'failed' ? 'danger' : 'success'}`;

    const main = document.createElement('div');
    main.className = 'account-main';

    const avatar = document.createElement('span');
    avatar.className = 'account-avatar';
    avatar.textContent = accountInitial(account.username);
    main.appendChild(avatar);

    const text = document.createElement('div');
    text.className = 'account-text';

    const heading = document.createElement('div');
    heading.className = 'account-heading';

    const title = document.createElement('strong');
    title.textContent = account.username || '未命名账号';
    heading.appendChild(title);
    heading.appendChild(createPremiumBadge(account));

    const premiumUntil = formatPremiumUntil(account.premium_until);
    if (premiumUntil) {
      heading.appendChild(createStatusPill(`到期 ${premiumUntil}`, 'neutral', true));
    }

    text.appendChild(heading);

    const meta = document.createElement('div');
    meta.className = 'muted';
    meta.textContent = account.logged_in ? 'session 已保存，可直接用于解析' : '将使用账号密码重新登录';
    text.appendChild(meta);

    if (account.last_error) {
      const error = document.createElement('p');
      error.className = 'account-error';
      error.textContent = account.last_error;
      text.appendChild(error);
    }

    main.appendChild(text);

    const side = document.createElement('div');
    side.className = 'account-side';
    side.appendChild(createStatusPill(account.status === 'failed' ? '失败' : '可用', account.status === 'failed' ? 'danger' : 'success'));

    const actions = document.createElement('div');
    actions.className = 'mini-actions';

    const resetButton = document.createElement('button');
    resetButton.type = 'button';
    resetButton.className = 'secondary compact';
    resetButton.disabled = account.status !== 'failed';
    setButtonContent(resetButton, '重置', 'refresh');
    resetButton.addEventListener('click', () => resetAccount(account.id));
    actions.appendChild(resetButton);

    const deleteButton = document.createElement('button');
    deleteButton.type = 'button';
    deleteButton.className = 'danger-button compact';
    setButtonContent(deleteButton, '删除', 'trash');
    deleteButton.addEventListener('click', () => deleteAccount(account.id));
    actions.appendChild(deleteButton);

    side.appendChild(actions);
    card.appendChild(main);
    card.appendChild(side);
    accountList.appendChild(card);
  }
}

function createPremiumBadge(account) {
  if (account.premium) {
    return createStatusPill(account.premium_type ? `会员 ${account.premium_type}` : '会员', 'success', true);
  }
  if (account.premium_error) {
    const badge = createStatusPill('会员未知', 'warn', true);
    badge.title = account.premium_error;
    return badge;
  }
  return createStatusPill('非会员', 'neutral', true);
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
  setButtonLabel(submitButton, state.resolveBusy ? '处理中...' : '开始解析');

  accountSubmitButton.disabled = state.accountBusy;
  setButtonLabel(accountSubmitButton, state.accountBusy ? '登录中...' : '添加并登录');
}

function renderPasswordPanel() {
  const fixed = Boolean(state.config?.password_fixed);
  passwordFixedNote.classList.toggle('hidden', !fixed);
  [currentPassword, newPassword, confirmPassword, passwordSubmitButton].forEach((el) => {
    if (el) el.disabled = fixed;
  });
}

async function onChangePassword(event) {
  event.preventDefault();
  hidePasswordError();

  const current = currentPassword.value;
  const next = newPassword.value;
  const confirm = confirmPassword.value;

  if (next.length < 6) {
    showPasswordError('新密码至少 6 位');
    return;
  }
  if (next !== confirm) {
    showPasswordError('两次输入的新密码不一致');
    return;
  }
  if (next === current) {
    showPasswordError('新密码不能与当前密码相同');
    return;
  }

  passwordSubmitButton.disabled = true;
  setButtonLabel(passwordSubmitButton, '修改中...');
  try {
    await api('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password: current, new_password: next }),
    });
    currentPassword.value = '';
    newPassword.value = '';
    confirmPassword.value = '';
    showToast('访问密码已修改', 'success');
  } catch (error) {
    showPasswordError(error.message);
  } finally {
    passwordSubmitButton.disabled = false;
    setButtonLabel(passwordSubmitButton, '修改密码');
  }
}

function showPasswordError(message) {
  passwordFormError.textContent = message;
  passwordFormError.classList.remove('hidden');
}

function hidePasswordError() {
  passwordFormError.textContent = '';
  passwordFormError.classList.add('hidden');
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
    showToast('账号已添加', 'success');
  } catch (error) {
    showAccountError(error.message);
  } finally {
    setAccountBusy(false);
  }
}

async function deleteAccount(id) {
  const account = state.accounts.find((item) => item.id === id);
  if (!account) return;

  if (!window.confirm(`确定删除账号 "${account.username}" 吗？此操作不可撤销。`)) {
    return;
  }

  try {
    await api(`/api/accounts/${id}`, { method: 'DELETE' });
    await refreshAppState();
    showToast('账号已删除', 'success');
  } catch (error) {
    showAccountError(error.message);
  }
}

async function resetAccount(id) {
  try {
    await api(`/api/accounts/${id}/reset`, { method: 'POST' });
    await refreshAppState();
    showToast('账号状态已重置', 'success');
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
  if (job.status === 'running' || job.status === 'queued') {
    jobBadge.classList.add('is-running');
  }

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
    const variant = attemptClass(attempt.status);
    const row = document.createElement('article');
    row.className = `attempt-item ${variant}`;

    const main = document.createElement('div');
    main.className = 'attempt-main';

    const name = document.createElement('strong');
    name.textContent = attempt.username || attempt.account_id || '未知账号';
    main.appendChild(name);

    const meta = document.createElement('div');
    meta.className = 'muted';
    meta.textContent = attempt.status === 'running' ? '正在尝试该账号' : '账号尝试记录';
    main.appendChild(meta);

    const badge = createStatusPill(humanizeAttempt(attempt.status), variant);
    if (attempt.status === 'running') {
      badge.classList.add('is-running');
    }

    row.appendChild(main);
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
    setButtonContent(button, job.stage === 'source_selection' ? '转存这一项' : '生成链接', 'play');
    button.addEventListener('click', () => chooseItem(job.id, item.id, button));

    row.appendChild(info);
    row.appendChild(button);
    selectionList.appendChild(row);
  }
}

async function chooseItem(jobId, itemId, button) {
  const previousText = getButtonLabel(button);
  button.disabled = true;
  setButtonLabel(button, '处理中...');

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
    setButtonLabel(button, previousText);
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

  requestAnimationFrame(() => {
    resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  });
}

function startLogPolling() {
  stopLogPolling();
  fetchLogs();
  logPollTimer = window.setInterval(fetchLogs, 1600);
}

function ensureLogPolling() {
  if (logPollTimer === null) {
    startLogPolling();
  }
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
    showToast('日志已清理', 'success');
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    clearLogsButton.disabled = false;
  }
}

// --- Version & update ---

function startUpdateStatusPolling() {
  fetchUpdateStatus();
  if (updateStatusTimer === null) {
    updateStatusTimer = window.setInterval(fetchUpdateStatus, 60000);
  }
}

function stopUpdateStatusPolling() {
  if (updateStatusTimer !== null) {
    window.clearInterval(updateStatusTimer);
    updateStatusTimer = null;
  }
}

function isUpdateBusy(status) {
  return ['checking', 'downloading', 'verifying', 'installing', 'restarting'].includes(status.phase);
}

async function fetchUpdateStatus() {
  if (updateRestartPending) {
    await watchForRestart();
    return;
  }
  try {
    const status = await api('/api/update');
    state.update = status;
    renderUpdate(status);
    syncUpdatePolling(status);
  } catch (error) {
    // Transient failures (e.g. brief network blips) are ignored; the next tick retries.
  }
}

// watchForRestart waits out the update restart window. It reloads once it has
// seen the server go down and come back, so it never reloads into the
// still-running old process. It probes the unauthenticated auth-status endpoint
// because the in-memory session is wiped by the restart. A deadline guards
// against a restart so fast the down-state is never observed, or one that never
// comes back.
async function watchForRestart() {
  try {
    await api('/api/auth/status');
    if (updateServerWentDown || (updateRestartDeadline && Date.now() > updateRestartDeadline)) {
      finishRestart();
    }
  } catch (error) {
    updateServerWentDown = true;
  }
}

function finishRestart() {
  updateRestartPending = false;
  updateServerWentDown = false;
  updateRestartDeadline = 0;
  stopUpdatePolling();
  showToast('更新完成，服务已重启', 'success');
  window.setTimeout(() => window.location.reload(), 900);
}

function syncUpdatePolling(status) {
  if (status.phase === 'restarting' && !updateRestartPending) {
    updateRestartPending = true;
    // Force a reload if the server doesn't visibly cycle within the window
    // (e.g. a restart fast enough that the down-state is never sampled).
    updateRestartDeadline = Date.now() + 90000;
  }
  const active = ['downloading', 'verifying', 'installing', 'restarting'].includes(status.phase);
  if (active || updateRestartPending) {
    startUpdatePolling();
  } else {
    stopUpdatePolling();
  }
}

function startUpdatePolling() {
  if (updatePollTimer !== null) return;
  updatePollTimer = window.setInterval(fetchUpdateStatus, 800);
}

function stopUpdatePolling() {
  if (updatePollTimer !== null) {
    window.clearInterval(updatePollTimer);
    updatePollTimer = null;
  }
}

async function onCheckUpdate() {
  checkUpdateButton.disabled = true;
  setButtonLabel(checkUpdateButton, '检查中...');
  try {
    const status = await api('/api/update/check', { method: 'POST' });
    state.update = status;
    renderUpdate(status);
    syncUpdatePolling(status);
    if (status.error) {
      showToast(status.error, 'error');
    } else if (status.update_available) {
      showToast(`发现新版本 ${status.latest_version}`, 'info');
    } else {
      showToast('已是最新版本', 'success');
    }
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    setButtonLabel(checkUpdateButton, '检查更新');
    renderUpdate(state.update || {});
  }
}

async function onInstallUpdate() {
  const status = state.update;
  if (!status || !status.update_available) return;
  if (!window.confirm(`确定更新到 ${status.latest_version} 吗？\n更新完成后服务会自动重启。`)) {
    return;
  }

  installUpdateButton.disabled = true;
  try {
    const next = await api('/api/update/install', { method: 'POST' });
    state.update = next;
    renderUpdate(next);
    showToast('开始下载更新 ...', 'info');
    startUpdatePolling();
  } catch (error) {
    showToast(error.message, 'error');
    renderUpdate(state.update || {});
  }
}

function renderUpdate(status) {
  status = status || {};
  const phase = status.phase || 'idle';
  const busy = isUpdateBusy(status);

  if (updateNavDot) {
    updateNavDot.hidden = !status.update_available;
  }

  updateCurrentVersion.textContent = status.current_version || '-';
  updateLatestVersion.textContent = status.latest_version || '-';
  updatePlatform.textContent = status.platform || '-';
  updateCheckedAt.textContent = status.checked_at ? formatDateTime(status.checked_at) : '-';

  const pill = updateStatusInfo(phase, status);
  updateStatusPill.textContent = pill.label;
  updateStatusPill.className = `status-pill ${pill.variant}`;
  if (busy) {
    updateStatusPill.classList.add('is-running');
  }

  if (status.error) {
    updateError.textContent = status.error;
    updateError.classList.remove('hidden');
  } else {
    updateError.classList.add('hidden');
  }

  renderUpdateProgress(status);

  checkUpdateButton.disabled = busy;
  installUpdateButton.disabled = !status.update_available || busy || status.managed === false;
  setButtonLabel(installUpdateButton, busy ? '更新中...' : '立即更新');

  if (status.release_url) {
    updateReleaseLink.href = status.release_url;
    updateReleaseLink.classList.remove('hidden');
  } else {
    updateReleaseLink.classList.add('hidden');
  }

  if (status.release_notes) {
    updateNotesBody.textContent = status.release_notes;
    updateNotes.classList.remove('hidden');
  } else {
    updateNotes.classList.add('hidden');
  }
}

function renderUpdateProgress(status) {
  const phase = status.phase;
  const show = ['downloading', 'verifying', 'installing', 'restarting'].includes(phase);
  if (!show) {
    updateProgress.classList.add('hidden');
    return;
  }
  updateProgress.classList.remove('hidden');

  if (phase === 'downloading') {
    const pct = Math.max(0, Math.min(100, Number(status.progress) || 0));
    updateProgressFill.classList.remove('indeterminate');
    updateProgressFill.style.width = `${pct}%`;
    updateProgressLabel.textContent = '下载中';
    updateProgressPercent.textContent = `${Math.round(pct)}%`;
    const total = Number(status.total_bytes) || 0;
    const done = Number(status.downloaded_bytes) || 0;
    updateProgressMeta.textContent = total > 0
      ? `${formatBytes(done)} / ${formatBytes(total)}`
      : (done > 0 ? formatBytes(done) : '');
  } else {
    updateProgressFill.classList.add('indeterminate');
    updateProgressFill.style.width = '';
    updateProgressPercent.textContent = '';
    updateProgressMeta.textContent = '';
    updateProgressLabel.textContent =
      phase === 'verifying' ? '校验完整性 ...'
        : phase === 'installing' ? '安装新版本 ...'
          : '正在重启服务 ...';
  }
}

function updateStatusInfo(phase, status) {
  switch (phase) {
    case 'checking':
      return { label: '检查中', variant: 'neutral' };
    case 'available':
      return { label: '有新版本', variant: 'warn' };
    case 'up_to_date':
      return { label: '已是最新', variant: 'success' };
    case 'downloading':
      return { label: '下载中', variant: 'neutral' };
    case 'verifying':
      return { label: '校验中', variant: 'neutral' };
    case 'installing':
      return { label: '安装中', variant: 'neutral' };
    case 'restarting':
      return { label: '重启中', variant: 'warn' };
    case 'error':
      return { label: '出错', variant: 'danger' };
    default:
      return status.update_available
        ? { label: '有新版本', variant: 'warn' }
        : { label: '空闲', variant: 'neutral' };
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
  if (!value) return;

  const original = getButtonLabel(button);
  try {
    await navigator.clipboard.writeText(value);
    setButtonLabel(button, '已复制');
    showToast('链接已复制', 'success');
  } catch {
    setButtonLabel(button, '复制失败');
    showToast('复制失败，请手动复制', 'error');
  }
  window.setTimeout(() => {
    setButtonLabel(button, original);
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
    case 'queued':
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
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString('zh-CN', { hour12: false });
}

function formatLogTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleTimeString('zh-CN', { hour12: false });
}

function formatPremiumUntil(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleDateString('zh-CN');
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

function redirectToGate() {
  stopLogPolling();
  stopPolling();
  stopUpdatePolling();
  stopUpdateStatusPolling();
  window.location.replace('/');
}

function renderAuthUI() {
  if (!logoutButton) return;
  const shouldShow = Boolean(state.config?.authenticated);
  logoutButton.style.display = shouldShow ? 'inline-flex' : 'none';
}

async function onLogout() {
  try {
    await api('/api/auth/logout', { method: 'POST' });
  } catch (error) {
    showToast(error.message, 'error');
    return;
  }
  redirectToGate();
}

function detectLinkType() {
  const input = resourceInput.value.trim().toLowerCase();
  if (!linkTypeIndicator) return;

  if (input === '') {
    linkTypeIndicator.textContent = '';
    linkTypeIndicator.className = 'link-type-indicator';
    return;
  }

  if (input.startsWith('magnet:?')) {
    linkTypeIndicator.textContent = '磁力链接';
    linkTypeIndicator.className = 'link-type-indicator valid';
  } else if (input.includes('pikpak.com/s/') || input.includes('mypikpak.com/s/')) {
    linkTypeIndicator.textContent = 'PikPak 分享链接';
    linkTypeIndicator.className = 'link-type-indicator valid';
  } else {
    linkTypeIndicator.textContent = '无法识别';
    linkTypeIndicator.className = 'link-type-indicator invalid';
  }
}

function showToast(message, level = 'info') {
  const toast = document.createElement('div');
  toast.className = `toast toast-${level}`;
  toast.textContent = message;
  document.body.appendChild(toast);

  window.setTimeout(() => toast.classList.add('show'), 10);
  window.setTimeout(() => {
    toast.classList.remove('show');
    window.setTimeout(() => toast.remove(), 300);
  }, 3000);
}

function animateCount(element, target) {
  if (!element) return;
  const from = Number(element.textContent.replace(/[^\d-]/g, '')) || 0;
  if (prefersReducedMotion || from === target) {
    element.textContent = String(target);
    return;
  }

  const duration = 580;
  const start = performance.now();
  const step = (now) => {
    const progress = Math.min(1, (now - start) / duration);
    const eased = 1 - Math.pow(1 - progress, 3);
    element.textContent = String(Math.round(from + (target - from) * eased));
    if (progress < 1) {
      requestAnimationFrame(step);
    }
  };
  requestAnimationFrame(step);
}

function spawnRipple(event) {
  if (prefersReducedMotion) return;
  const target = event.target.closest('button, .secondary-link');
  if (!target || target.disabled) return;

  const rect = target.getBoundingClientRect();
  const size = Math.max(rect.width, rect.height);
  const ripple = document.createElement('span');
  ripple.className = 'ripple';
  ripple.style.width = ripple.style.height = `${size}px`;
  ripple.style.left = `${event.clientX - rect.left - size / 2}px`;
  ripple.style.top = `${event.clientY - rect.top - size / 2}px`;
  target.appendChild(ripple);
  ripple.addEventListener('animationend', () => ripple.remove());
}

function createStatusPill(text, variant = 'neutral', compact = false) {
  const badge = document.createElement('span');
  badge.className = `status-pill ${variant}${compact ? ' compact-pill' : ''}`;
  badge.textContent = text;
  return badge;
}

function createIcon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.classList.add('ui-icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', `#icon-${name}`);
  svg.appendChild(use);
  return svg;
}

function setButtonContent(button, label, iconName) {
  button.textContent = '';
  if (iconName) {
    button.appendChild(createIcon(iconName));
  }
  const span = document.createElement('span');
  span.className = 'button-label';
  span.textContent = label;
  button.appendChild(span);
}

function setButtonLabel(button, label) {
  const labelNode = button.querySelector('.button-label');
  if (labelNode) {
    labelNode.textContent = label;
  } else {
    button.textContent = label;
  }
}

function getButtonLabel(button) {
  return button.querySelector('.button-label')?.textContent || button.textContent.trim();
}

function accountInitial(username) {
  const value = String(username || '').trim();
  if (!value) return '?';
  return value.charAt(0).toUpperCase();
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
