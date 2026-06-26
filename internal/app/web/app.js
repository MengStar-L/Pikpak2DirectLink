const navButtons = Array.from(document.querySelectorAll('.nav-button[data-page]'));
const bootOverlay = document.getElementById('bootOverlay');
const resolvePage = document.getElementById('resolvePage');
const accountsPage = document.getElementById('accountsPage');
const logsPage = document.getElementById('logsPage');

const logoutButton = document.getElementById('logoutButton');
const metricAccountCount = document.getElementById('metricAccountCount');
const metricAvailableCount = document.getElementById('metricAvailableCount');
const metricFailedCount = document.getElementById('metricFailedCount');
const metricRunningCount = document.getElementById('metricRunningCount');
const metricWaitingCount = document.getElementById('metricWaitingCount');

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
const selectionTree = document.getElementById('selectionTree');
const selectAll = document.getElementById('selectAll');
const selectionSummary = document.getElementById('selectionSummary');
const generateButton = document.getElementById('generateButton');
const resultPanel = document.getElementById('resultPanel');
const resultMode = document.getElementById('resultMode');
const resultSingle = document.getElementById('resultSingle');
const resultTree = document.getElementById('resultTree');
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
const accountTrafficLimit = document.getElementById('accountTrafficLimit');
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

const concurrencyForm = document.getElementById('concurrencyForm');
const concurrencyInput = document.getElementById('concurrencyInput');
const taskTimeoutInput = document.getElementById('taskTimeoutInput');
const concurrencyState = document.getElementById('concurrencyState');
const concurrencySubmitButton = document.getElementById('concurrencySubmitButton');
const concurrencyFormError = document.getElementById('concurrencyFormError');

const clearLogsButton = document.getElementById('clearLogsButton');
const logList = document.getElementById('logList');

const updatePage = document.getElementById('updatePage');
const settingsPage = document.getElementById('settingsPage');
const cdkPage = document.getElementById('cdkPage');
const cdkGenForm = document.getElementById('cdkGenForm');
const cdkCount = document.getElementById('cdkCount');
const cdkRemaining = document.getElementById('cdkRemaining');
const cdkDays = document.getElementById('cdkDays');
const cdkGenSubmit = document.getElementById('cdkGenSubmit');
const cdkGenError = document.getElementById('cdkGenError');
const cdkList = document.getElementById('cdkList');
const cdkPurgeExpired = document.getElementById('cdkPurgeExpired');
const cdkPortalLink = document.getElementById('cdkPortalLink');
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
  cdks: [],
  queue: { running: 0, waiting: 0 },
};

let currentJobId = null;
let pollTimer = null;
// Latest resolved links, tracked so the aria2 "push all" button can read them.
let lastResults = null;
let lastSingleResult = null;
// The aria2 "push all" button, created by mountAria2. Declared here (above the
// boot() call) so the synchronous mountAria2() invocation can assign it without
// hitting a temporal-dead-zone error.
let aria2PushAll = null;
let logPollTimer = null;
let queuePollTimer = null;
let updatePollTimer = null;
let updateStatusTimer = null;
let updateRestartPending = false;
let updateServerWentDown = false;
let updateRestartDeadline = 0;
let accountErrorOverlay = null;
let resourceWarningOverlay = null;
let lastResourceWarningJobId = null;
let checkboxByItemId = new Map();
let selectionStage = '';
let selectionJobId = null;

const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
const BAD_RESOURCE_PARSE_MESSAGE = '该磁链连续遇到解析错误，请不要反复重试此链接。';
const ADMIN_PAGE_STORAGE_KEY = 'pikpak2directlink.admin.currentPage';
const adminPages = {
  resolve: resolvePage,
  accounts: accountsPage,
  logs: logsPage,
  update: updatePage,
  settings: settingsPage,
  cdk: cdkPage,
};

boot();

async function boot() {
  bindActions();
  mountAria2();
  clearJobUI();
  const targetPage = loadAdminPage();
  const ready = await refreshAppState();
  if (!ready) return;

  showPage(targetPage);
  hideBootOverlay();
  if (state.authenticated) {
    startUpdateStatusPolling();
  }
}

// mountAria2 wires the shared aria2 helper into the resolve page: a config
// button in the status cluster (usable any time, even before resolving) and
// push buttons on the static single-result link rows. The result-tree push
// buttons are added inline by buildLinkRow.
function mountAria2() {
  if (!window.Aria2) return;

  const cluster = document.querySelector('#resolvePage .status-cluster');
  if (cluster) {
    cluster.insertBefore(window.Aria2.configButton(), cluster.firstChild);
  }

  // Single-result view: push buttons next to the existing copy buttons.
  directCopy?.parentElement?.appendChild(
    window.Aria2.pushButton(() => directValue.value, () => resultName.textContent),
  );
  proxyCopy?.parentElement?.appendChild(
    window.Aria2.pushButton(() => proxyValue.value, () => resultName.textContent),
  );

  // "Push all" lives in the result panel head; it pushes every resolved link
  // (preferring direct over proxy per file).
  if (resultMode?.parentElement) {
    aria2PushAll = document.createElement('button');
    aria2PushAll.type = 'button';
    aria2PushAll.className = 'secondary compact aria2-push-btn hidden';
    aria2PushAll.innerHTML =
      '<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 3 3 10.5l6.2 2.3L21 3Z"/><path d="m21 3-7.7 18-2.3-6.2"/></svg><span class="button-label">全部推送 aria2</span>';
    aria2PushAll.addEventListener('click', () => window.Aria2.pushMany(collectPushItems()));
    resultMode.parentElement.appendChild(aria2PushAll);
  }
}

// collectPushItems gathers one preferred link per resolved file from whichever
// result shape the current job produced.
function collectPushItems() {
  const items = [];
  const push = (result) => {
    if (!result) return;
    const url = result.url || result.direct_url || result.proxy_url;
    if (url) items.push({ url, name: result.file?.path || result.file?.name });
  };
  if (lastResults && lastResults.length) {
    lastResults.forEach(push);
  } else if (lastSingleResult) {
    push(lastSingleResult);
  }
  return items;
}

function refreshAria2PushAll() {
  if (!aria2PushAll) return;
  const count = collectPushItems().length;
  aria2PushAll.classList.toggle('hidden', count < 2);
}

function bindActions() {
  navButtons.forEach((button) => {
    button.addEventListener('click', () => showPage(button.dataset.page));
  });
  document.addEventListener('pointerdown', spawnRipple);
  resolveForm.addEventListener('submit', onResolveSubmit);
  accountForm.addEventListener('submit', onAccountSubmit);
  passwordForm.addEventListener('submit', onChangePassword);
  concurrencyForm?.addEventListener('submit', onSaveConcurrency);
  selectAll?.addEventListener('change', onSelectAll);
  generateButton?.addEventListener('click', onGenerateSelection);
  logoutButton?.addEventListener('click', onLogout);
  clearLogsButton.addEventListener('click', clearLogs);
  checkUpdateButton.addEventListener('click', onCheckUpdate);
  installUpdateButton.addEventListener('click', onInstallUpdate);
  cdkGenForm.addEventListener('submit', onGenerateCDKs);
  cdkPurgeExpired?.addEventListener('click', onPurgeExpiredCDKs);
  directCopy.addEventListener('click', () => copyText(directValue.value, directCopy));
  proxyCopy.addEventListener('click', () => copyText(proxyValue.value, proxyCopy));
  resourceInput.addEventListener('input', detectLinkType);
}

function showPage(page) {
  if (!adminPages[page]) return false;

  state.currentPage = page;
  persistAdminPage(page);
  Object.entries(adminPages).forEach(([name, element]) => {
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
  if (page === 'cdk') {
    fetchCDKs();
  }
  if (page === 'settings') {
    fetchSettings();
  }
  return true;
}

function loadAdminPage() {
  try {
    const page = window.localStorage.getItem(ADMIN_PAGE_STORAGE_KEY);
    return adminPages[page] ? page : 'resolve';
  } catch {
    return 'resolve';
  }
}

function persistAdminPage(page) {
  if (!adminPages[page]) return;
  try {
    window.localStorage.setItem(ADMIN_PAGE_STORAGE_KEY, page);
  } catch {
    // Private browsing or blocked storage should not break navigation.
  }
}

function hideBootOverlay() {
  if (!bootOverlay || bootOverlay.classList.contains('hidden') || bootOverlay.classList.contains('closing')) {
    return;
  }

  const finish = () => {
    bootOverlay.classList.add('hidden');
    bootOverlay.classList.remove('closing');
  };

  if (prefersReducedMotion) {
    finish();
    return;
  }

  bootOverlay.classList.add('closing');
  bootOverlay.addEventListener('animationend', function handler(event) {
    if (event.target !== bootOverlay) return;
    bootOverlay.removeEventListener('animationend', handler);
    finish();
  });
}

async function refreshAppState() {
  try {
    const config = await api('/api/config');
    state.config = config;
    state.authenticated = Boolean(config.authenticated);
    renderAuthUI();

    if (config.auth_required && !config.authenticated) {
      redirectToGate();
      return false;
    }

    const accountPayload = await api('/api/accounts');
    state.accounts = accountPayload.accounts || [];
    renderMetrics();
    renderAccounts();
    renderPasswordPanel();
    renderAvailability();
    renderAuthUI();
    ensureLogPolling();
    ensureQueuePolling();
    return true;
  } catch (error) {
    if (error.message.includes('authentication required')) {
      redirectToGate();
      return false;
    }
    showToast(error.message, 'error');
    return true;
  }
}

function renderMetrics() {
  const total = state.accounts.length;
  const failed = state.accounts.filter((account) => account.status === 'failed').length;
  const available = Math.max(0, total - failed);

  animateCount(metricAccountCount, total);
  animateCount(metricAvailableCount, available);
  animateCount(metricFailedCount, failed);
  renderQueueMetrics();

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

// renderQueueMetrics paints the live resolution counters (running / waiting) in
// the global status bar. The data is kept fresh by ensureQueuePolling.
function renderQueueMetrics() {
  animateCount(metricRunningCount, Number(state.queue?.running) || 0);
  animateCount(metricWaitingCount, Number(state.queue?.waiting) || 0);
}

async function fetchQueueMetrics() {
  try {
    const settings = await api('/api/settings');
    state.queue = { running: settings.running || 0, waiting: settings.waiting || 0 };
    renderQueueMetrics();
  } catch {
    // Transient errors (e.g. a momentary 401 during logout) just skip a tick.
  }
}

function startQueuePolling() {
  stopQueuePolling();
  fetchQueueMetrics();
  queuePollTimer = window.setInterval(fetchQueueMetrics, 3000);
}

function ensureQueuePolling() {
  if (queuePollTimer === null) {
    startQueuePolling();
  }
}

function stopQueuePolling() {
  if (queuePollTimer !== null) {
    window.clearInterval(queuePollTimer);
    queuePollTimer = null;
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
    const cardTone = account.status === 'failed' ? 'danger' : (account.traffic_limited ? 'warn' : 'success');
    const card = document.createElement('article');
    card.className = `account-card ${cardTone}`;

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

    heading.appendChild(createStatusPill(
      `本月下行 ${formatBytes(account.traffic_used)} / ${formatBytes(account.traffic_limit)}`,
      account.traffic_limited ? 'warn' : 'neutral',
      true,
    ));

    text.appendChild(heading);

    const meta = document.createElement('div');
    meta.className = 'muted account-meta-line';
    const accountUsable = account.status !== 'failed';
    meta.appendChild(createAccountMetaPill(accountUsable ? '可用' : '不可用', accountUsable ? 'success' : 'danger'));
    meta.appendChild(createAccountMetaPill(
      `上次验证：${account.credential_checked_at ? formatDateTime(account.credential_checked_at) : '尚未验证'}`,
      'checked',
    ));
    meta.appendChild(createAccountMetaPill(
      `下次验证：${account.credential_next_check_at ? formatDateTime(account.credential_next_check_at) : '-'}`,
      'next',
    ));
    const parseErrorCount = Number(account.parse_error_count) || 0;
    if (parseErrorCount > 0) {
      const parseButton = document.createElement('button');
      parseButton.type = 'button';
      parseButton.className = 'account-parse-error-pill';
      parseButton.title = '查看解析错误';
      parseButton.appendChild(createIcon('alert'));
      const label = document.createElement('span');
      label.textContent = `收到 ${parseErrorCount} 条解析错误`;
      parseButton.appendChild(label);
      parseButton.addEventListener('click', () => showAccountParseErrors(account));
      meta.appendChild(parseButton);
    }
    text.appendChild(meta);

    if (account.last_error) {
      const error = document.createElement('p');
      error.className = 'account-error';
      error.textContent = account.last_error;
      text.appendChild(error);
    }
    if (account.credential_check_error && account.credential_check_error !== account.last_error) {
      const error = document.createElement('p');
      error.className = 'account-error';
      error.textContent = `凭据验证：${account.credential_check_error}`;
      text.appendChild(error);
    }

    main.appendChild(text);

    const side = document.createElement('div');
    side.className = 'account-side';
    const [statusLabel, statusTone] = account.status === 'failed'
      ? ['失败', 'danger']
      : (account.traffic_limited ? ['到达限行流量', 'warn'] : ['可用', 'success']);
    side.appendChild(createStatusPill(statusLabel, statusTone));

    // Inline editor for the monthly traffic limit (in GB).
    const limitEdit = document.createElement('div');
    limitEdit.className = 'mini-actions';
    const limitGB = Math.max(1, Math.round((Number(account.traffic_limit) || 0) / (1024 ** 3)));
    const limitField = cdkNumberField('上限GB', limitGB, 1);
    const saveLimitButton = document.createElement('button');
    saveLimitButton.type = 'button';
    saveLimitButton.className = 'secondary compact';
    setButtonContent(saveLimitButton, '改额度', 'check');
    saveLimitButton.addEventListener('click', () => saveAccountLimit(account.id, Number(limitField.input.value), saveLimitButton));
    limitEdit.appendChild(limitField.wrap);
    limitEdit.appendChild(saveLimitButton);
    side.appendChild(limitEdit);

    const actions = document.createElement('div');
    actions.className = 'mini-actions';

    const refreshLoginButton = document.createElement('button');
    refreshLoginButton.type = 'button';
    refreshLoginButton.className = 'secondary compact';
    setButtonContent(refreshLoginButton, '刷新登录', 'refresh');
    refreshLoginButton.addEventListener('click', () => refreshAccountLogin(account.id, refreshLoginButton));
    actions.appendChild(refreshLoginButton);

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

function showAccountParseErrors(account) {
  const overlay = ensureAccountErrorOverlay();
  const title = overlay.querySelector('.account-error-modal-title');
  const subtitle = overlay.querySelector('.account-error-modal-subtitle');
  const list = overlay.querySelector('.account-error-history');
  const errors = Array.isArray(account.parse_errors) ? account.parse_errors : [];

  title.textContent = '解析错误记录';
  subtitle.textContent = account.username || '未命名账号';
  list.innerHTML = '';

  if (errors.length === 0) {
    const empty = document.createElement('li');
    empty.className = 'account-error-history-empty';
    empty.textContent = '暂无错误详情';
    list.appendChild(empty);
  } else {
    for (const entry of errors) {
      const item = document.createElement('li');
      const meta = document.createElement('span');
      meta.className = 'account-error-history-meta';
      meta.textContent = [formatDateTime(entry.time), entry.job_id ? `任务 ${entry.job_id}` : ''].filter(Boolean).join(' · ');
      const message = document.createElement('strong');
      message.textContent = entry.message || 'record not found';
      item.appendChild(meta);
      item.appendChild(message);
      list.appendChild(item);
    }
  }

  overlay.classList.remove('closing');
  overlay.classList.remove('hidden');
}

function ensureAccountErrorOverlay() {
  if (accountErrorOverlay) return accountErrorOverlay;

  const overlay = document.createElement('div');
  overlay.className = 'account-error-overlay hidden';

  const modal = document.createElement('div');
  modal.className = 'account-error-modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-label', '账号解析错误记录');

  const head = document.createElement('div');
  head.className = 'account-error-modal-head';
  const titleWrap = document.createElement('div');
  const title = document.createElement('h2');
  title.className = 'account-error-modal-title';
  const subtitle = document.createElement('p');
  subtitle.className = 'muted account-error-modal-subtitle';
  titleWrap.appendChild(title);
  titleWrap.appendChild(subtitle);

  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'account-error-modal-close';
  close.setAttribute('aria-label', '关闭');
  close.textContent = '×';

  head.appendChild(titleWrap);
  head.appendChild(close);

  const list = document.createElement('ul');
  list.className = 'account-error-history';

  modal.appendChild(head);
  modal.appendChild(list);
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  overlay.addEventListener('click', (event) => {
    if (event.target === overlay) {
      closeAccountErrorOverlay();
    }
  });
  close.addEventListener('click', closeAccountErrorOverlay);
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !overlay.classList.contains('hidden')) {
      closeAccountErrorOverlay();
    }
  });

  accountErrorOverlay = overlay;
  return overlay;
}

function closeAccountErrorOverlay() {
  const overlay = accountErrorOverlay;
  if (!overlay || overlay.classList.contains('hidden') || overlay.classList.contains('closing')) {
    return;
  }
  overlay.classList.add('closing');
  if (prefersReducedMotion) {
    overlay.classList.add('hidden');
    overlay.classList.remove('closing');
    return;
  }
  overlay.addEventListener('animationend', function handler(event) {
    if (event.target !== overlay) return;
    overlay.removeEventListener('animationend', handler);
    overlay.classList.add('hidden');
    overlay.classList.remove('closing');
  });
}

function maybeShowResourceWarning(job) {
  const labels = badResourceFailureLabels(job);
  const hasWarning = job.error === BAD_RESOURCE_PARSE_MESSAGE || labels.length > 0;
  if (!hasWarning || job.id === lastResourceWarningJobId) {
    return;
  }
  lastResourceWarningJobId = job.id;
  showResourceWarning(labels);
}

function badResourceFailureLabels(job) {
  const failures = Array.isArray(job.batch?.failures) ? job.batch.failures : [];
  return failures
    .filter((failure) => failure.error === BAD_RESOURCE_PARSE_MESSAGE)
    .map((failure) => failure.label)
    .filter(Boolean);
}

function showResourceWarning(labels = []) {
  const overlay = ensureResourceWarningOverlay();
  const detail = overlay.querySelector('.resource-warning-detail');
  if (detail) {
    detail.textContent = labels.length ? `失败链接：${labels.join('、')}` : '';
    detail.classList.toggle('hidden', labels.length === 0);
  }
  overlay.classList.remove('closing');
  overlay.classList.remove('hidden');
}

function ensureResourceWarningOverlay() {
  if (resourceWarningOverlay) return resourceWarningOverlay;

  const overlay = document.createElement('div');
  overlay.className = 'resource-warning-overlay hidden';

  const modal = document.createElement('div');
  modal.className = 'resource-warning-modal';
  modal.setAttribute('role', 'alertdialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-label', '链接解析失败提醒');

  const mark = document.createElement('div');
  mark.className = 'resource-warning-mark';
  mark.textContent = '!';

  const title = document.createElement('h2');
  title.textContent = '此链接解析失败';
  const message = document.createElement('p');
  message.className = 'resource-warning-message';
  message.textContent = BAD_RESOURCE_PARSE_MESSAGE;
  const detail = document.createElement('p');
  detail.className = 'resource-warning-detail hidden';

  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'primary';
  setButtonLabel(close, '知道了');
  close.addEventListener('click', closeResourceWarning);

  modal.appendChild(mark);
  modal.appendChild(title);
  modal.appendChild(message);
  modal.appendChild(detail);
  modal.appendChild(close);
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  overlay.addEventListener('click', (event) => {
    if (event.target === overlay) {
      closeResourceWarning();
    }
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !overlay.classList.contains('hidden')) {
      closeResourceWarning();
    }
  });

  resourceWarningOverlay = overlay;
  return overlay;
}

function closeResourceWarning() {
  const overlay = resourceWarningOverlay;
  if (!overlay || overlay.classList.contains('hidden') || overlay.classList.contains('closing')) {
    return;
  }
  overlay.classList.add('closing');
  if (prefersReducedMotion) {
    overlay.classList.add('hidden');
    overlay.classList.remove('closing');
    return;
  }
  overlay.addEventListener('animationend', function handler(event) {
    if (event.target !== overlay) return;
    overlay.removeEventListener('animationend', handler);
    overlay.classList.add('hidden');
    overlay.classList.remove('closing');
  });
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

function renderSettings(settings) {
  if (!settings) return;
  if (document.activeElement !== concurrencyInput) {
    concurrencyInput.value = settings.concurrency;
  }
  concurrencyInput.max = settings.max_concurrency || 32;
  const timeout = Number(settings.task_timeout_seconds) ||
    (settings.parallel ? settings.parallel_timeout_seconds : settings.serial_timeout_seconds);
  const minTimeoutMinutes = Math.max(1, Math.ceil((Number(settings.min_task_timeout_seconds) || 60) / 60));
  if (document.activeElement !== taskTimeoutInput) {
    taskTimeoutInput.value = Math.max(minTimeoutMinutes, Math.ceil((timeout || 60) / 60));
  }
  taskTimeoutInput.min = minTimeoutMinutes;
  taskTimeoutInput.max = Math.max(minTimeoutMinutes, Math.floor((Number(settings.max_task_timeout_seconds) || 43200) / 60));
  const mode = settings.parallel ? `并行 ×${settings.concurrency}` : '串行';
  concurrencyState.textContent = `${mode} · 任务超时 ${formatSeconds(timeout)}`;
  // Keep the global status-bar counters in sync with whatever this response saw.
  state.queue = { running: settings.running || 0, waiting: settings.waiting || 0 };
  renderQueueMetrics();
}

function formatSeconds(seconds) {
  const s = Number(seconds) || 0;
  if (s % 60 === 0) return `${s / 60} 分钟`;
  if (s < 60) return `${s} 秒`;
  return `${Math.floor(s / 60)} 分 ${s % 60} 秒`;
}

async function fetchSettings() {
  hideConcurrencyError();
  try {
    const settings = await api('/api/settings');
    renderSettings(settings);
  } catch (error) {
    showConcurrencyError(error.message);
  }
}

async function onSaveConcurrency(event) {
  event.preventDefault();
  hideConcurrencyError();

  const value = Number(concurrencyInput.value);
  const max = Number(concurrencyInput.max) || 32;
  const timeoutMinutes = Number(taskTimeoutInput.value);
  const minTimeoutMinutes = Number(taskTimeoutInput.min) || 1;
  const maxTimeoutMinutes = Number(taskTimeoutInput.max) || 720;
  if (!Number.isInteger(value) || value < 1) {
    showConcurrencyError('并发数至少为 1');
    return;
  }
  if (value > max) {
    showConcurrencyError(`并发数最多为 ${max}`);
    return;
  }
  if (!Number.isInteger(timeoutMinutes) || timeoutMinutes < minTimeoutMinutes) {
    showConcurrencyError(`任务超时时间至少为 ${minTimeoutMinutes} 分钟`);
    return;
  }
  if (timeoutMinutes > maxTimeoutMinutes) {
    showConcurrencyError(`任务超时时间最多为 ${maxTimeoutMinutes} 分钟`);
    return;
  }

  concurrencySubmitButton.disabled = true;
  setButtonLabel(concurrencySubmitButton, '保存中...');
  try {
    const settings = await api('/api/settings', {
      method: 'PUT',
      body: JSON.stringify({
        concurrency: value,
        task_timeout_seconds: timeoutMinutes * 60,
      }),
    });
    renderSettings(settings);
    const mode = settings.parallel ? `并行解析 ×${settings.concurrency}` : '串行解析';
    showToast(`${mode} · 超时 ${formatSeconds(settings.task_timeout_seconds)}`, 'success');
  } catch (error) {
    showConcurrencyError(error.message);
  } finally {
    concurrencySubmitButton.disabled = false;
    setButtonLabel(concurrencySubmitButton, '保存设置');
  }
}

function showConcurrencyError(message) {
  concurrencyFormError.textContent = message;
  concurrencyFormError.classList.remove('hidden');
}

function hideConcurrencyError() {
  concurrencyFormError.textContent = '';
  concurrencyFormError.classList.add('hidden');
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
        traffic_limit_gb: Math.max(1, Number(accountTrafficLimit.value) || 700),
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

async function refreshAccountLogin(id, button) {
  const previous = getButtonLabel(button) || '刷新登录';
  button.disabled = true;
  setButtonContent(button, '刷新中', 'refresh');
  try {
    await api(`/api/accounts/${id}/refresh-login`, { method: 'POST' });
    await refreshAppState();
    showToast('PikPak 登录信息已刷新', 'success');
  } catch (error) {
    showAccountError(error.message);
    button.disabled = false;
    setButtonContent(button, previous, 'refresh');
  }
}

async function saveAccountLimit(id, gb, button) {
  if (!(gb >= 1)) {
    showToast('流量额度至少为 1G', 'error');
    return;
  }
  button.disabled = true;
  setButtonContent(button, '保存中', 'check');
  try {
    await api(`/api/accounts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ traffic_limit_gb: Math.round(gb) }),
    });
    await refreshAppState();
    showToast('流量额度已更新', 'success');
  } catch (error) {
    showToast(error.message, 'error');
    button.disabled = false;
    setButtonContent(button, '改额度', 'check');
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

  if (job.status === 'queued') {
    const ahead = Number(job.queue_ahead) || 0;
    jobMessage.textContent = ahead > 0
      ? `排队中：前方还有 ${ahead} 条任务`
      : '排队中：等待当前任务完成…';
  } else {
    jobMessage.textContent = job.message || '处理中...';
  }
  jobMessage.classList.remove('hidden');
  renderAttempts(job.account_attempts || []);

  if (job.error) {
    showJobError(job.error);
    maybeShowResourceWarning(job);
  } else {
    hideJobError();
    maybeShowResourceWarning(job);
  }

  if (job.status === 'selection_required') {
    renderSelection(job);
  } else {
    selectionPanel.classList.add('hidden');
    selectionTree.innerHTML = '';
    selectionJobId = null;
    selectionStage = '';
    checkboxByItemId = new Map();
  }

  // A batch job (or any job carrying several results) renders a folder tree where
  // each link is a sibling top-level folder; a single-result job keeps the
  // detailed single-link panel.
  const results = job.results && job.results.length ? job.results : null;
  if (results) {
    renderResultTree(job, results);
  } else if (job.result) {
    renderResult(job);
  } else if (hasJobNotices(job)) {
    renderResultNoticesOnly(job);
  } else {
    resultPanel.classList.add('hidden');
    clearResultNotices();
  }
}

function jobWarnings(job) {
  return Array.isArray(job?.warnings) ? job.warnings.filter(Boolean) : [];
}

function jobFailures(job) {
  return Array.isArray(job?.batch?.failures) ? job.batch.failures.filter(Boolean) : [];
}

function hasJobNotices(job) {
  return jobWarnings(job).length > 0 || jobFailures(job).length > 0;
}

function ensureResultNotices() {
  let notices = resultPanel.querySelector('.result-notices');
  if (notices) return notices;
  notices = document.createElement('div');
  notices.className = 'result-notices hidden';
  const before = resultSingle && resultSingle.parentElement === resultPanel ? resultSingle : resultTree;
  resultPanel.insertBefore(notices, before || null);
  return notices;
}

function clearResultNotices() {
  const notices = resultPanel.querySelector('.result-notices');
  if (!notices) return;
  notices.innerHTML = '';
  notices.classList.add('hidden');
}

function renderResultNotices(job) {
  const warnings = jobWarnings(job);
  const failures = jobFailures(job);
  const notices = ensureResultNotices();
  notices.innerHTML = '';
  if (warnings.length === 0 && failures.length === 0) {
    notices.classList.add('hidden');
    return;
  }

  appendNoticeBlock(notices, 'warn', '提示', warnings);
  appendNoticeBlock(notices, 'danger', '失败链接', failures.map((failure) => {
    const label = failure.label || '链接';
    return `${label}: ${failure.error || '解析失败'}`;
  }));
  notices.classList.remove('hidden');
}

function appendNoticeBlock(container, variant, title, items) {
  if (!items.length) return;
  const block = document.createElement('section');
  block.className = `result-notice ${variant}`;
  const heading = document.createElement('strong');
  heading.textContent = title;
  block.appendChild(heading);
  const list = document.createElement('ul');
  for (const item of items) {
    const li = document.createElement('li');
    li.textContent = item;
    list.appendChild(li);
  }
  block.appendChild(list);
  container.appendChild(block);
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
  if (selectionJobId === job.id && selectionStage === job.stage) {
    return;
  }
  selectionJobId = job.id;
  selectionStage = job.stage;
  selectionPanel.classList.remove('hidden');
  checkboxByItemId = new Map();
  selectionTree.innerHTML = '';

  const items = job.items || [];
  const sourceSelection = job.stage === 'source_selection';
  selectionHint.textContent = sourceSelection
    ? '勾选要解析的文件，可多选'
    : '勾选需要生成下载链接的文件，可多选';
  selectAll.closest('.tree-toolbar').classList.remove('hidden');
  generateButton.parentElement.classList.remove('hidden');

  const root = buildSelectionTree(items);
  for (const node of root.children) {
    selectionTree.appendChild(renderSelectionNode(node, sourceSelection));
  }
  selectAll.checked = false;
  selectAll.indeterminate = false;
  updateSelectionSummary();
}

function buildSelectionTree(items) {
  const root = { name: '', children: [], childMap: new Map(), isFolder: true };
  for (const item of items) {
    const parts = (item.path || item.name || '').split('/').filter(Boolean);
    let node = root;
    for (let i = 0; i < parts.length - 1; i += 1) {
      const seg = parts[i];
      let next = node.childMap.get(seg);
      if (!next) {
        next = { name: seg, children: [], childMap: new Map(), isFolder: true };
        node.childMap.set(seg, next);
        node.children.push(next);
      }
      node = next;
    }
    const leafName = parts.length ? parts[parts.length - 1] : (item.name || '未命名');
    const leaf = { name: leafName, item, children: [], childMap: new Map(), isFolder: false };
    node.childMap.set(leafName + ':' + item.id, leaf);
    node.children.push(leaf);
  }
  return root;
}

function renderSelectionNode(node, sourceSelection) {
  if (node.isFolder) {
    const wrap = document.createElement('div');
    wrap.className = 'tree-folder';

    const row = document.createElement('div');
    row.className = 'tree-row tree-folder-row';

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'tree-toggle';
    toggle.innerHTML = '<svg class="ui-icon"><use href="#icon-chevron"></use></svg>';

    const childrenWrap = document.createElement('div');
    childrenWrap.className = 'tree-children';
    const childrenInner = document.createElement('div');
    childrenInner.className = 'tree-children-inner';
    childrenWrap.appendChild(childrenInner);

    let open = true;
    toggle.addEventListener('click', () => {
      open = !open;
      childrenWrap.classList.toggle('collapsed', !open);
      toggle.classList.toggle('collapsed', !open);
    });

    const label = document.createElement('span');
    label.className = 'tree-label';
    label.innerHTML = '<svg class="ui-icon tree-folder-icon"><use href="#icon-folder"></use></svg>';
    const folderName = document.createElement('span');
    folderName.textContent = node.name;
    label.appendChild(folderName);

    row.appendChild(toggle);
    const folderCheck = document.createElement('input');
    folderCheck.type = 'checkbox';
    folderCheck.className = 'tree-checkbox';
    folderCheck.addEventListener('change', () => {
      popCheckbox(folderCheck);
      setSubtreeChecked(node, folderCheck.checked);
      refreshFolderStates();
      updateSelectionSummary();
    });
    row.appendChild(folderCheck);
    row.appendChild(label);
    wrap.appendChild(row);

    for (const child of node.children) {
      childrenInner.appendChild(renderSelectionNode(child, sourceSelection));
    }
    wrap.appendChild(childrenWrap);
    return wrap;
  }

  const row = document.createElement('div');
  row.className = 'tree-row tree-file-row';
  const label = document.createElement('label');
  label.className = 'tree-label tree-file-label';

  const check = document.createElement('input');
  check.type = 'checkbox';
  check.className = 'tree-checkbox';
  check.addEventListener('change', () => {
    popCheckbox(check);
    refreshFolderStates();
    updateSelectionSummary();
  });
  checkboxByItemId.set(node.item.id, check);

  label.innerHTML = '<svg class="ui-icon tree-file-icon"><use href="#icon-file"></use></svg>';
  const name = document.createElement('span');
  name.className = 'tree-file-name';
  name.textContent = node.name;
  label.appendChild(name);

  const meta = document.createElement('span');
  meta.className = 'tree-file-meta';
  meta.textContent = formatBytes(node.item.size);

  row.appendChild(check);
  row.appendChild(label);
  row.appendChild(meta);
  if (sourceSelection) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'secondary compact';
    setButtonLabel(button, '解析');
    button.addEventListener('click', () => chooseSelectionItem(selectionJobId, node.item.id, button));
    row.appendChild(button);
  }
  return row;
}

function popCheckbox(box) {
  box.classList.remove('pop');
  void box.offsetWidth;
  box.classList.add('pop');
}

function setSubtreeChecked(node, checked) {
  for (const child of node.children) {
    if (child.isFolder) {
      setSubtreeChecked(child, checked);
    } else {
      const cb = checkboxByItemId.get(child.item.id);
      if (cb) cb.checked = checked;
    }
  }
}

function refreshFolderStates() {
  const folders = selectionTree.querySelectorAll('.tree-folder');
  folders.forEach((el) => {
    const checks = el.querySelectorAll('.tree-file-row .tree-checkbox');
    const folderCheck = el.querySelector(':scope > .tree-folder-row .tree-checkbox');
    if (!folderCheck) return;
    let total = 0;
    let on = 0;
    checks.forEach((c) => { total += 1; if (c.checked) on += 1; });
    folderCheck.checked = total > 0 && on === total;
    folderCheck.indeterminate = on > 0 && on < total;
  });

  const all = [...checkboxByItemId.values()];
  const on = all.filter((c) => c.checked).length;
  selectAll.checked = all.length > 0 && on === all.length;
  selectAll.indeterminate = on > 0 && on < all.length;
}

function onSelectAll() {
  for (const cb of checkboxByItemId.values()) cb.checked = selectAll.checked;
  selectAll.indeterminate = false;
  refreshFolderStates();
  updateSelectionSummary();
}

function selectedItemIds() {
  const ids = [];
  for (const [id, cb] of checkboxByItemId) {
    if (cb.checked) ids.push(id);
  }
  return ids;
}

function updateSelectionSummary() {
  const count = selectedItemIds().length;
  selectionSummary.textContent = `已选 ${count} 项`;
  setButtonLabel(generateButton, `解析已选中（${count}项）`);
  generateButton.disabled = count === 0;
}

async function onGenerateSelection() {
  const ids = selectedItemIds();
  if (!ids.length || !selectionJobId) return;
  generateButton.disabled = true;
  setButtonLabel(generateButton, '解析中...');
  try {
    const job = await api(`/api/jobs/${selectionJobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_ids: ids }),
    });
    currentJobId = job.id;
    selectionPanel.classList.add('hidden');
    renderJob(job);
    setResolveBusy(true);
    startPolling();
  } catch (error) {
    showJobError(error.message);
    generateButton.disabled = false;
  } finally {
    updateSelectionSummary();
  }
}

async function chooseSelectionItem(jobId, itemId, button) {
  const previousText = getButtonLabel(button);
  button.disabled = true;
  setButtonLabel(button, '处理中...');

  try {
    const job = await api(`/api/jobs/${jobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_ids: [itemId] }),
    });
    renderJob(job);
    currentJobId = job.id;
    selectionPanel.classList.add('hidden');
    setResolveBusy(true);
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
  lastResults = null;
  lastSingleResult = result;
  resultPanel.classList.remove('hidden');
  resultSingle.classList.remove('hidden');
  resultTree.classList.add('hidden');
  resultTree.innerHTML = '';
  renderResultNotices(job);

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
  reorderSingleResultLinks(job.mode);

  refreshAria2PushAll();
  requestAnimationFrame(() => {
    resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  });
}

// renderResultTree shows a multi-result job as a read-only folder tree. Each
// link's files live under their own sibling top-level folder (the server already
// prefixes result paths with "链接N ..."), so the tree groups them naturally.
function renderResultTree(job, results) {
  lastResults = results;
  lastSingleResult = null;
  resultPanel.classList.remove('hidden');
  resultSingle.classList.add('hidden');
  resultTree.classList.remove('hidden');
  resultTree.innerHTML = '';
  renderResultNotices(job);

  const batch = job.batch;
  if (batch) {
    const failed = Number(batch.failed) || 0;
    resultMode.textContent = failed > 0
      ? `解析成功 ${batch.succeeded || 0}/${batch.total || 0} 条 · 失败 ${failed} 条`
      : `解析成功 ${batch.succeeded || 0}/${batch.total || 0} 条`;
    resultMode.className = `status-pill ${(batch.succeeded || 0) > 0 ? 'success' : 'danger'}`;
  } else {
    resultMode.textContent = `${results.length} 个文件`;
    resultMode.className = 'status-pill success';
  }

  const root = buildResultTree(results);
  for (const node of root.children) {
    resultTree.appendChild(renderResultNode(node, job.mode));
  }

  refreshAria2PushAll();
  requestAnimationFrame(() => {
    resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  });
}

// buildResultTree turns flat results (with file.path like "链接1 名/夹/文件.mkv")
// into a nested folder structure. Leaves carry the JobResult for link rendering.
function buildResultTree(results) {
  const root = { name: '', children: [], childMap: new Map(), isFolder: true };
  for (const result of results) {
    const file = result.file || {};
    const parts = (file.path || file.name || '未命名').split('/').filter(Boolean);
    let node = root;
    for (let i = 0; i < parts.length - 1; i += 1) {
      const seg = parts[i];
      let next = node.childMap.get(seg);
      if (!next) {
        next = { name: seg, children: [], childMap: new Map(), isFolder: true };
        node.childMap.set(seg, next);
        node.children.push(next);
      }
      node = next;
    }
    const leafName = parts.length ? parts[parts.length - 1] : (file.name || '未命名');
    const leaf = { name: leafName, result, children: [], childMap: new Map(), isFolder: false };
    node.childMap.set(leafName + ':' + (result.proxy_token || result.direct_url || ''), leaf);
    node.children.push(leaf);
  }
  return root;
}

function renderResultNoticesOnly(job) {
  lastResults = null;
  lastSingleResult = null;
  resultPanel.classList.remove('hidden');
  resultSingle.classList.add('hidden');
  resultTree.classList.add('hidden');
  resultTree.innerHTML = '';
  resultMode.textContent = '任务提示';
  resultMode.className = 'status-pill warn';
  renderResultNotices(job);
  refreshAria2PushAll();
}

function renderResultNode(node, mode = 'direct') {
  if (node.isFolder) {
    const wrap = document.createElement('div');
    wrap.className = 'tree-folder';

    const row = document.createElement('div');
    row.className = 'tree-row tree-folder-row';

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'tree-toggle';
    toggle.innerHTML = '<svg class="ui-icon"><use href="#icon-chevron"></use></svg>';

    const childrenWrap = document.createElement('div');
    childrenWrap.className = 'tree-children';
    const childrenInner = document.createElement('div');
    childrenInner.className = 'tree-children-inner';
    childrenWrap.appendChild(childrenInner);

    let open = true;
    toggle.addEventListener('click', () => {
      open = !open;
      childrenWrap.classList.toggle('collapsed', !open);
      toggle.classList.toggle('collapsed', !open);
    });

    const label = document.createElement('span');
    label.className = 'tree-label';
    label.innerHTML = '<svg class="ui-icon tree-folder-icon"><use href="#icon-folder"></use></svg>';
    const folderName = document.createElement('span');
    folderName.textContent = node.name;
    label.appendChild(folderName);

    row.appendChild(toggle);
    row.appendChild(label);
    wrap.appendChild(row);

    for (const child of node.children) {
      childrenInner.appendChild(renderResultNode(child, mode));
    }
    wrap.appendChild(childrenWrap);
    return wrap;
  }

  // Leaf: a resolved file with its link rows.
  const result = node.result;
  const card = document.createElement('div');
  card.className = 'tree-result';

  const head = document.createElement('div');
  head.className = 'tree-row tree-file-row';
  head.innerHTML = '<svg class="ui-icon tree-file-icon"><use href="#icon-file"></use></svg>';
  const name = document.createElement('span');
  name.className = 'tree-file-name';
  name.textContent = node.name;
  head.appendChild(name);
  const meta = document.createElement('span');
  meta.className = 'tree-file-meta';
  meta.textContent = formatBytes(result.file?.size);
  head.appendChild(meta);
  card.appendChild(head);

  appendResultLinks(card, result, mode);
  return card;
}

function reorderSingleResultLinks(mode = 'direct') {
  const directBlock = directValue?.closest('.link-block');
  const proxyBlock = proxyValue?.closest('.link-block');
  if (!directBlock || !proxyBlock || !resultSingle) return;
  if (mode === 'proxy') {
    resultSingle.appendChild(proxyBlock);
    resultSingle.appendChild(directBlock);
    return;
  }
  resultSingle.appendChild(directBlock);
  resultSingle.appendChild(proxyBlock);
}

function appendResultLinks(container, result, mode = 'direct') {
  const direct = { tag: '直链', cls: 'direct', url: result.direct_url };
  const proxy = { tag: '代理链接', cls: 'proxy', url: result.proxy_url };
  const rows = mode === 'proxy' ? [proxy, direct] : [direct, proxy];
  for (const row of rows) {
    if (row.url) {
      container.appendChild(buildLinkRow(row.tag, row.cls, row.url, result.file?.path || result.file?.name));
    }
  }
}

// buildLinkRow is the compact open/copy block used for each link in the result
// tree. When aria2 is available it also gets a "推送" button that sends this
// link straight to the user's aria2 RPC endpoint.
function buildLinkRow(tagText, tagClass, url, name) {
  const block = document.createElement('div');
  block.className = 'link-block';

  const row = document.createElement('div');
  row.className = 'link-row';
  const tag = document.createElement('span');
  tag.className = `link-tag ${tagClass}`;
  tag.textContent = tagText;
  row.appendChild(tag);

  const actions = document.createElement('div');
  actions.className = 'link-actions';
  const open = document.createElement('a');
  open.className = 'secondary-link compact';
  open.href = url;
  open.target = '_blank';
  open.rel = 'noreferrer noopener';
  open.innerHTML = '<svg class="ui-icon"><use href="#icon-open"></use></svg>打开';
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'secondary compact';
  copy.innerHTML = '<svg class="ui-icon"><use href="#icon-copy"></use></svg><span class="button-label">复制</span>';
  copy.addEventListener('click', () => copyText(url, copy));
  actions.appendChild(open);
  actions.appendChild(copy);
  if (window.Aria2) {
    actions.appendChild(window.Aria2.pushButton(url, name));
  }
  row.appendChild(actions);
  block.appendChild(row);

  const input = document.createElement('input');
  input.className = 'link-input';
  input.type = 'text';
  input.readOnly = true;
  input.value = url;
  block.appendChild(input);
  return block;
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
      return;
    }

    for (const entry of nextLogs) {
      state.logs.push(entry);
      state.lastLogId = Math.max(state.lastLogId, Number(entry.id) || 0);
    }
    if (state.logs.length > 500) {
      state.logs = state.logs.slice(-500);
    }
    if (state.currentPage === 'logs') {
      appendLogs(nextLogs);
    }
  } catch {
    stopLogPolling();
  }
}

// True when the viewport is at (or within a hair of) the bottom of the list.
function logsAtBottom() {
  return logList.scrollHeight - logList.scrollTop - logList.clientHeight < 40;
}

function createLogRow(entry) {
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
  return row;
}

// Full rebuild — used on page navigation and after clearing. Always lands at
// the bottom since this is an explicit (re)entry into the log view.
function renderLogs() {
  logList.innerHTML = '';

  if (state.logs.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'console-empty';
    empty.textContent = '暂无日志';
    logList.appendChild(empty);
    return;
  }

  const fragment = document.createDocumentFragment();
  for (const entry of state.logs) {
    fragment.appendChild(createLogRow(entry));
  }
  logList.appendChild(fragment);
  logList.scrollTop = logList.scrollHeight;
}

// Incremental append — only the new rows are created, so existing rows never
// re-render (no flicker). Scroll follows the tail only when the user was
// already at the bottom; otherwise their scroll position is preserved.
function appendLogs(entries) {
  if (!entries.length) {
    return;
  }

  const placeholder = logList.querySelector('.console-empty');
  if (placeholder) {
    placeholder.remove();
  }

  const stick = logsAtBottom();

  const fragment = document.createDocumentFragment();
  for (const entry of entries) {
    fragment.appendChild(createLogRow(entry));
  }
  logList.appendChild(fragment);

  // Keep the DOM in sync with the 500-entry cap on state.logs.
  while (logList.children.length > 500) {
    logList.removeChild(logList.firstElementChild);
  }

  if (stick) {
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

// --- CDK distribution (admin) ---

async function fetchCDKs() {
  try {
    const payload = await api('/api/cdks');
    state.cdks = payload.cdks || [];
    renderCDKs();
  } catch (error) {
    showToast(error.message, 'error');
  }
}

async function onGenerateCDKs(event) {
  event.preventDefault();
  hideCdkGenError();

  const count = Number(cdkCount.value);
  const trafficGB = Number(cdkRemaining.value);
  const days = Number(cdkDays.value);
  if (!(count >= 1)) { showCdkGenError('分发数量至少为 1'); return; }
  if (!(trafficGB >= 1)) { showCdkGenError('流量额度至少为 1G'); return; }
  if (!(days >= 1)) { showCdkGenError('到期天数至少为 1'); return; }

  cdkGenSubmit.disabled = true;
  setButtonLabel(cdkGenSubmit, '生成中...');
  try {
    const payload = await api('/api/cdks', {
      method: 'POST',
      body: JSON.stringify({ count, traffic_gb: trafficGB, days }),
    });
    const generated = payload.cdks || [];
    await fetchCDKs();

    const codes = generated.map((cdk) => cdk.code).join('\n');
    let copied = false;
    if (codes) {
      try {
        await navigator.clipboard.writeText(codes);
        copied = true;
      } catch {
        // clipboard may be unavailable (insecure context / denied); ignore
      }
    }
    showToast(`已生成 ${generated.length} 个 CDK${copied ? '，已复制到剪贴板' : ''}`, 'success');
  } catch (error) {
    showCdkGenError(error.message);
  } finally {
    cdkGenSubmit.disabled = false;
    setButtonLabel(cdkGenSubmit, '生成 CDK');
  }
}

async function saveCDK(code, trafficGB, days, button) {
  if (!(trafficGB >= 0) || !(days >= 1)) {
    showToast('流量额度不能为负，天数至少为 1', 'error');
    return;
  }
  button.disabled = true;
  const original = getButtonLabel(button);
  setButtonLabel(button, '保存中...');
  try {
    await api(`/api/cdks/${encodeURIComponent(code)}`, {
      method: 'PATCH',
      body: JSON.stringify({ traffic_gb: trafficGB, days }),
    });
    await fetchCDKs();
    showToast('CDK 已更新', 'success');
  } catch (error) {
    showToast(error.message, 'error');
    button.disabled = false;
    setButtonLabel(button, original);
  }
}

async function deleteCDK(code) {
  if (!window.confirm(`确定删除 CDK ${code} 吗？此操作不可撤销。`)) return;
  try {
    await api(`/api/cdks/${encodeURIComponent(code)}`, { method: 'DELETE' });
    await fetchCDKs();
    showToast('CDK 已删除', 'success');
  } catch (error) {
    showToast(error.message, 'error');
  }
}

async function onPurgeExpiredCDKs() {
  const expiredCount = state.cdks.filter((cdk) => cdk.expired).length;
  if (expiredCount === 0) {
    showToast('没有已过期的 CDK', 'info');
    return;
  }
  if (!window.confirm(`确定删除全部 ${expiredCount} 个已过期 CDK 吗？此操作不可撤销。`)) return;

  cdkPurgeExpired.disabled = true;
  const original = getButtonLabel(cdkPurgeExpired);
  setButtonLabel(cdkPurgeExpired, '清理中...');
  try {
    const payload = await api('/api/cdks/expired', { method: 'DELETE' });
    await fetchCDKs();
    showToast(`已清理 ${payload.deleted || 0} 个过期 CDK`, 'success');
  } catch (error) {
    showToast(error.message, 'error');
  } finally {
    cdkPurgeExpired.disabled = false;
    setButtonLabel(cdkPurgeExpired, original);
  }
}

function renderCDKs() {
  cdkList.innerHTML = '';
  if (state.cdks.length === 0) {
    cdkList.className = 'cdk-list empty';
    cdkList.textContent = '还没有分发任何 CDK';
    return;
  }
  cdkList.className = 'cdk-list';
  for (const cdk of state.cdks) {
    cdkList.appendChild(buildCDKCard(cdk));
  }
}

function buildCDKCard(cdk) {
  const card = document.createElement('article');
  card.className = `cdk-card ${cdk.expired ? 'danger' : 'success'}`;

  // left group: ticket stub + code/meta + status pill
  const id = document.createElement('div');
  id.className = 'cdk-id';
  const stub = document.createElement('span');
  stub.className = 'cdk-stub';
  stub.setAttribute('aria-hidden', 'true');
  stub.appendChild(createIcon('ticket'));
  const text = document.createElement('div');
  text.className = 'cdk-text';
  const code = document.createElement('code');
  code.className = 'cdk-code';
  code.textContent = cdk.code;
  const meta = document.createElement('div');
  meta.className = 'muted';
  meta.textContent = `剩余 ${cdk.remaining_label} · 已用 ${cdk.used_label} · 创建于 ${formatDateTime(cdk.created_at)}`;
  text.appendChild(code);
  text.appendChild(meta);
  id.appendChild(stub);
  id.appendChild(text);
  id.appendChild(createStatusPill(cdk.expired ? '已过期' : `有效 ${cdk.days_left} 天`, cdk.expired ? 'danger' : 'success'));

  // right group: editable fields + save, then copy/delete actions
  const ops = document.createElement('div');
  ops.className = 'cdk-ops';

  const edit = document.createElement('div');
  edit.className = 'cdk-edit';
  const remGB = Math.round((Number(cdk.remaining_bytes) || 0) / (1024 ** 3));
  const remField = cdkNumberField('剩余流量(GB)', remGB, 0);
  const dayField = cdkNumberField('到期天数', cdk.expired ? 1 : cdk.days_left, 1);
  const saveBtn = document.createElement('button');
  saveBtn.type = 'button';
  saveBtn.className = 'secondary compact';
  setButtonContent(saveBtn, '保存', 'check');
  saveBtn.addEventListener('click', () => saveCDK(cdk.code, Number(remField.input.value), Number(dayField.input.value), saveBtn));
  edit.appendChild(remField.wrap);
  edit.appendChild(dayField.wrap);
  edit.appendChild(saveBtn);

  const actions = document.createElement('div');
  actions.className = 'mini-actions';
  const copyCodeBtn = document.createElement('button');
  copyCodeBtn.type = 'button';
  copyCodeBtn.className = 'secondary compact';
  setButtonContent(copyCodeBtn, '复制码', 'copy');
  copyCodeBtn.addEventListener('click', () => copyText(cdk.code, copyCodeBtn));
  const copyLinkBtn = document.createElement('button');
  copyLinkBtn.type = 'button';
  copyLinkBtn.className = 'secondary compact';
  setButtonContent(copyLinkBtn, '复制链接', 'open');
  copyLinkBtn.addEventListener('click', () => copyText(`${location.origin}/u?code=${cdk.code}`, copyLinkBtn));
  const delBtn = document.createElement('button');
  delBtn.type = 'button';
  delBtn.className = 'danger-button compact';
  setButtonContent(delBtn, '删除', 'trash');
  delBtn.addEventListener('click', () => deleteCDK(cdk.code));
  actions.appendChild(copyCodeBtn);
  actions.appendChild(copyLinkBtn);
  actions.appendChild(delBtn);

  ops.appendChild(edit);
  ops.appendChild(actions);

  card.appendChild(id);
  card.appendChild(ops);
  return card;
}

function cdkNumberField(label, value, min) {
  const wrap = document.createElement('label');
  wrap.className = 'cdk-field';
  const span = document.createElement('span');
  span.textContent = label;
  const input = document.createElement('input');
  input.type = 'number';
  input.min = String(min);
  input.value = String(value);
  wrap.appendChild(span);
  wrap.appendChild(input);
  return { wrap, input };
}

function showCdkGenError(message) {
  cdkGenError.textContent = message;
  cdkGenError.classList.remove('hidden');
}

function hideCdkGenError() {
  cdkGenError.textContent = '';
  cdkGenError.classList.add('hidden');
}

function clearJobUI() {
  stopPolling();
  currentJobId = null;
  lastResourceWarningJobId = null;
  lastResults = null;
  lastSingleResult = null;
  refreshAria2PushAll();
  hideJobError();
  selectionPanel.classList.add('hidden');
  selectionTree.innerHTML = '';
  selectionJobId = null;
  selectionStage = '';
  checkboxByItemId = new Map();
  resultPanel.classList.add('hidden');
  clearResultNotices();
  if (resultTree) resultTree.innerHTML = '';
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
  stopQueuePolling();
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
  if (!linkTypeIndicator) return;
  const raw = resourceInput.value;
  const lines = raw.split('\n').map((line) => line.trim()).filter(Boolean);

  if (lines.length === 0) {
    linkTypeIndicator.textContent = '';
    linkTypeIndicator.className = 'link-type-indicator';
    return;
  }

  const classify = (line) => {
    const lower = line.toLowerCase();
    if (lower.startsWith('magnet:?')) return 'magnet';
    if (lower.includes('pikpak.com/s/') || lower.includes('mypikpak.com/s/')) return 'share';
    return 'invalid';
  };

  // Multi-line submission: report the count and flag any unrecognized line.
  if (lines.length > 1) {
    const kinds = lines.map(classify);
    const bad = kinds.filter((k) => k === 'invalid').length;
    if (bad > 0) {
      linkTypeIndicator.textContent = `${lines.length} 条链接 · ${bad} 条无法识别`;
      linkTypeIndicator.className = 'link-type-indicator invalid';
    } else {
      linkTypeIndicator.textContent = `${lines.length} 条链接`;
      linkTypeIndicator.className = 'link-type-indicator valid';
    }
    return;
  }

  const kind = classify(lines[0]);
  if (kind === 'magnet') {
    linkTypeIndicator.textContent = '磁力链接';
    linkTypeIndicator.className = 'link-type-indicator valid';
  } else if (kind === 'share') {
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

function createAccountMetaPill(text, variant = 'neutral') {
  const badge = document.createElement('span');
  badge.className = `account-meta-pill ${variant}`;
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
