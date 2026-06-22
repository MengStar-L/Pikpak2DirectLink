const gateView = document.getElementById('gateView');
const appView = document.getElementById('appView');
const cdkForm = document.getElementById('cdkForm');
const cdkInput = document.getElementById('cdkInput');
const cdkSubmit = document.getElementById('cdkSubmit');
const cdkError = document.getElementById('cdkError');

const cdkCodeLabel = document.getElementById('cdkCodeLabel');
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
const selectionTree = document.getElementById('selectionTree');
const selectAll = document.getElementById('selectAll');
const selectionSummary = document.getElementById('selectionSummary');
const generateButton = document.getElementById('generateButton');

const resultPanel = document.getElementById('resultPanel');
const resultCount = document.getElementById('resultCount');
const resultList = document.getElementById('resultList');

let currentJobId = null;
let pollTimer = null;
let statusTimer = null;
let resolveBusy = false;
// Latest resolved links, tracked so the aria2 "push all" button can read them.
let lastResults = [];
let aria2PushAll = null;
let resourceWarningOverlay = null;
let lastResourceWarningJobId = null;

// Selection state for the current selection_required job. checkboxByItemId maps
// a file's item id to its <input> so select-all and tristate folders can drive
// them; selectionStage distinguishes source (single transfer pick) from result
// (multi-file) selection.
let checkboxByItemId = new Map();
let selectionStage = '';
let selectionJobId = null;

const BAD_RESOURCE_PARSE_MESSAGE = '该磁链连续遇到解析错误，请不要反复重试此链接。';
const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

boot();

async function boot() {
  cdkForm.addEventListener('submit', onCdkSubmit);
  logoutButton.addEventListener('click', onLogout);
  resolveForm.addEventListener('submit', onResolveSubmit);
  selectAll.addEventListener('change', onSelectAll);
  generateButton.addEventListener('click', onGenerate);
  mountAria2();

  const presetCode = new URLSearchParams(location.search).get('code');
  if (presetCode) {
    // A code in the URL wins over any existing session cookie: logging in
    // rebinds the cookie to this code, so the header reflects the CDK from the
    // link rather than whoever logged in last on this browser.
    cdkInput.value = presetCode.trim().toUpperCase();
    cdkForm.requestSubmit();
    return;
  }

  try {
    const status = await api('/api/u/status');
    enterApp(status);
  } catch {
    showGate();
  }
}

function showGate() {
  stopStatusPolling();
  appView.classList.add('hidden');
  gateView.classList.remove('hidden');
  playViewEnter(gateView);
  cdkInput.focus();
}

// mountAria2 adds the aria2 config button to the always-visible status bar and a
// "push all" button to the result panel head. Per-link push buttons are added by
// buildLinkRow. Safe to call once at boot; no-ops if the helper failed to load.
function mountAria2() {
  if (!window.Aria2) return;

  const status = document.querySelector('.cdk-status');
  if (status && logoutButton) {
    status.insertBefore(window.Aria2.configButton(), logoutButton);
  }

  const head = document.querySelector('#resultPanel .panel-head');
  if (head) {
    aria2PushAll = document.createElement('button');
    aria2PushAll.type = 'button';
    aria2PushAll.className = 'secondary compact aria2-push-btn hidden';
    aria2PushAll.innerHTML =
      '<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 3 3 10.5l6.2 2.3L21 3Z"/><path d="m21 3-7.7 18-2.3-6.2"/></svg><span class="button-label">全部推送 aria2</span>';
    aria2PushAll.addEventListener('click', () => window.Aria2.pushMany(collectPushItems()));
    head.appendChild(aria2PushAll);
  }
}

// collectPushItems returns one preferred link per resolved file.
function collectPushItems() {
  const items = [];
  for (const result of lastResults || []) {
    const url = result.url || result.direct_url || result.proxy_url;
    if (url) items.push({ url, name: result.file?.path || result.file?.name });
  }
  return items;
}

function refreshAria2PushAll() {
  if (!aria2PushAll) return;
  aria2PushAll.classList.toggle('hidden', collectPushItems().length < 2);
}

function enterApp(status) {
  gateView.classList.add('hidden');
  appView.classList.remove('hidden');
  playViewEnter(appView);
  renderStatus(status);
  startStatusPolling();
}

// playViewEnter restarts the entrance animation each time a view is shown.
function playViewEnter(el) {
  el.classList.remove('view-enter');
  void el.offsetWidth;
  el.classList.add('view-enter');
}

function renderStatus(status) {
  if (!status) return;
  if (status.code) cdkCodeLabel.textContent = status.code;
  cdkRemainingPill.textContent = `剩余 ${status.remaining_label || formatBytes(status.remaining_bytes)}`;
  cdkRemainingPill.className = `status-pill ${status.remaining_bytes > 0 ? 'success' : 'danger'}`;
  cdkDaysPill.textContent = status.expired ? '已过期' : `到期 ${status.days_left} 天`;
  cdkDaysPill.className = `status-pill ${status.expired ? 'danger' : 'neutral'}`;
  renderQueue(status.queue);

  const usable = status.remaining_bytes > 0 && !status.expired;
  resourceInput.disabled = !usable;
  passCodeInput.disabled = !usable;
  submitButton.disabled = !usable || resolveBusy;
  setButtonLabel(submitButton, resolveBusy ? '处理中...' : usable ? '开始解析' : '次数已用完或已过期');
}

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
    maybeShowResourceWarning(job);
  } else {
    hide(jobError);
    maybeShowResourceWarning(job);
  }

  if (job.status === 'selection_required') {
    renderSelection(job);
  } else {
    selectionPanel.classList.add('hidden');
  }

  const results = job.results && job.results.length ? job.results : (job.result ? [job.result] : []);
  if (results.length) {
    renderResults(job, results);
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
  resultPanel.insertBefore(notices, resultList || null);
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

// renderSelection builds a folder tree from the flat item list (paths like
// "图集/foo.mkv") with tristate folder checkboxes and a select-all.
function renderSelection(job) {
  if (selectionJobId === job.id && selectionStage === job.stage) {
    // Already rendered for this job/stage; don't rebuild and wipe the user's
    // checkbox state on every poll.
    return;
  }
  selectionJobId = job.id;
  selectionStage = job.stage;
  selectionPanel.classList.remove('hidden');
  checkboxByItemId = new Map();
  selectionTree.innerHTML = '';

  const items = job.items || [];
  const sourceSelection = job.stage === 'source_selection';
  selectionHint.textContent = sourceSelection ? '勾选要解析的文件，可多选' : '勾选需要生成下载链接的文件，可多选';

  selectAll.closest('.tree-toolbar').classList.remove('hidden');
  generateButton.parentElement.classList.remove('hidden');

  const root = buildTree(items);
  for (const node of root.children) {
    selectionTree.appendChild(renderNode(node, sourceSelection));
  }
  selectAll.checked = false;
  selectAll.indeterminate = false;
  updateSelectionSummary();
}

// buildTree turns flat {id,name,path,kind,size} items into a nested folder
// structure. Folders are inferred from "/"-separated paths.
function buildTree(items) {
  const root = { name: '', children: [], childMap: new Map() };
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

function renderNode(node, sourceSelection) {
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
    node._folderCheck = folderCheck;
    row.appendChild(folderCheck);
    row.appendChild(label);
    wrap.appendChild(row);

    for (const child of node.children) {
      childrenInner.appendChild(renderNode(child, sourceSelection));
    }
    wrap.appendChild(childrenWrap);
    return wrap;
  }

  // Leaf file row.
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
    button.addEventListener('click', () => chooseSingle(selectionJobId, node.item.id, button));
    row.appendChild(button);
  }
  return row;
}

// popCheckbox replays the brief scale animation each time a box is toggled.
function popCheckbox(box) {
  box.classList.remove('pop');
  void box.offsetWidth;
  box.classList.add('pop');
}

function setSubtreeChecked(node, checked) {
  for (const child of node.children) {
    if (child.isFolder) {
      setSubtreeChecked(child, checked);    } else {
      const cb = checkboxByItemId.get(child.item.id);
      if (cb) cb.checked = checked;
    }
  }
}

// refreshFolderStates recomputes every folder checkbox's checked/indeterminate
// flag from its descendant files, plus the master select-all.
function refreshFolderStates() {
  const folders = selectionTree.querySelectorAll('.tree-folder');
  // Walk deepest-first isn't required since we read leaf checkboxes directly.
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

async function onGenerate() {
  const ids = selectedItemIds();
  if (!ids.length) return;
  generateButton.disabled = true;
  setButtonLabel(generateButton, '解析中...');
  try {
    const job = await api(`/api/u/jobs/${selectionJobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_ids: ids }),
    });
    currentJobId = job.id;
    selectionPanel.classList.add('hidden');
    renderJob(job);
    setResolveBusy(true);
    startPolling();
  } catch (error) {
    showError(jobError, error.message);
    generateButton.disabled = false;
  } finally {
    updateSelectionSummary();
  }
}

async function chooseSingle(jobId, itemId, button) {
  button.disabled = true;
  const original = getButtonLabel(button);
  setButtonLabel(button, '处理中...');
  try {
    const job = await api(`/api/u/jobs/${jobId}/select`, {
      method: 'POST',
      body: JSON.stringify({ item_ids: [itemId] }),
    });
    currentJobId = job.id;
    selectionPanel.classList.add('hidden');
    renderJob(job);
    setResolveBusy(true);
    startPolling();
  } catch (error) {
    showError(jobError, error.message);
  } finally {
    button.disabled = false;
    setButtonLabel(button, original);
  }
}

// renderResults shows one card per resolved file, each with its direct and/or
// proxy link. For a batch job the cards are grouped by their top-level folder so
// each link's files sit under their own sibling section.
function renderResults(job, results) {
  lastResults = results || [];
  resultPanel.classList.remove('hidden');
  renderResultNotices(job);
  const batch = job.batch;
  resultCount.textContent = batch
    ? `${batch.succeeded || 0}/${batch.total || 0} 条${(Number(batch.failed) || 0) > 0 ? ` · 失败 ${Number(batch.failed) || 0} 条` : ''} · ${results.length} 个文件`
    : `${results.length} 个文件`;
  resultList.innerHTML = '';

  // Group by the first path segment. A batch link's results are pre-prefixed with
  // "链接N ..." by the server, so each group is one link; single jobs collapse to
  // one unlabeled group.
  const groups = new Map();
  for (const result of results) {
    const path = result.file?.path || result.file?.name || '';
    const parts = path.split('/').filter(Boolean);
    const groupName = batch && parts.length > 1 ? parts[0] : '';
    if (!groups.has(groupName)) groups.set(groupName, []);
    groups.get(groupName).push(result);
  }

  for (const [groupName, groupResults] of groups) {
    if (groupName) {
      const header = document.createElement('div');
      header.className = 'result-group-head';
      header.innerHTML = '<svg class="ui-icon tree-folder-icon"><use href="#icon-folder"></use></svg>';
      const label = document.createElement('span');
      label.textContent = `${groupName} · ${groupResults.length} 个文件`;
      header.appendChild(label);
      resultList.appendChild(header);
    }

    for (const result of groupResults) {
      const card = document.createElement('article');
      card.className = 'result-card';

      const head = document.createElement('div');
      head.className = 'result-card-head';
      const name = document.createElement('strong');
      name.textContent = result.file?.name || '-';
      head.appendChild(name);
      const meta = document.createElement('span');
      meta.className = 'muted';
      meta.textContent = [result.file?.path, formatBytes(result.file?.size)].filter(Boolean).join(' · ');
      head.appendChild(meta);
      card.appendChild(head);

      appendResultLinks(card, result, job.mode);
      resultList.appendChild(card);
    }
  }

  requestAnimationFrame(() => resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
  refreshAria2PushAll();
}

function renderResultNoticesOnly(job) {
  lastResults = [];
  resultPanel.classList.remove('hidden');
  resultCount.textContent = '任务提示';
  resultList.innerHTML = '';
  renderResultNotices(job);
  refreshAria2PushAll();
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

function clearJobUI() {
  stopPolling();
  currentJobId = null;
  lastResourceWarningJobId = null;
  selectionJobId = null;
  selectionStage = '';
  checkboxByItemId = new Map();
  lastResults = [];
  refreshAria2PushAll();
  hide(jobError);
  selectionPanel.classList.add('hidden');
  selectionTree.innerHTML = '';
  resultPanel.classList.add('hidden');
  clearResultNotices();
  resultList.innerHTML = '';
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
