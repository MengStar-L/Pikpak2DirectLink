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

// Selection state for the current selection_required job. checkboxByItemId maps
// a file's item id to its <input> so select-all and tristate folders can drive
// them; selectionStage distinguishes source (single transfer pick) from result
// (multi-file) selection.
let checkboxByItemId = new Map();
let selectionStage = '';
let selectionJobId = null;

boot();

async function boot() {
  cdkForm.addEventListener('submit', onCdkSubmit);
  logoutButton.addEventListener('click', onLogout);
  resolveForm.addEventListener('submit', onResolveSubmit);
  selectAll.addEventListener('change', onSelectAll);
  generateButton.addEventListener('click', onGenerate);

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
  } else {
    hide(jobError);
  }

  if (job.status === 'selection_required') {
    renderSelection(job);
  } else {
    selectionPanel.classList.add('hidden');
  }

  const results = job.results && job.results.length ? job.results : (job.result ? [job.result] : []);
  if (results.length) {
    renderResults(job, results);
  } else {
    resultPanel.classList.add('hidden');
  }
}

// renderSelection builds a folder tree from the flat item list (paths like
// "图集/foo.mkv") with tristate folder checkboxes and a select-all. Source
// selection only allows one item, so it falls back to single-pick buttons.
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
  const single = job.stage === 'source_selection';
  selectionHint.textContent = single ? '该分享含多个项目，请选择要转存的一项' : '勾选需要生成下载链接的文件，可多选';

  // Select-all / summary / generate button are only meaningful for multi-select.
  selectAll.closest('.tree-toolbar').classList.toggle('hidden', single);
  generateButton.parentElement.classList.toggle('hidden', single);

  const root = buildTree(items);
  for (const node of root.children) {
    selectionTree.appendChild(renderNode(node, single));
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

function renderNode(node, single) {
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
    if (!single) {
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
    }
    row.appendChild(label);
    wrap.appendChild(row);

    for (const child of node.children) {
      childrenInner.appendChild(renderNode(child, single));
    }
    wrap.appendChild(childrenWrap);
    return wrap;
  }

  // Leaf file row.
  const row = document.createElement('div');
  row.className = 'tree-row tree-file-row';
  const label = document.createElement('label');
  label.className = 'tree-label tree-file-label';

  if (single) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'secondary compact';
    setButtonLabel(button, '转存这一项');
    button.addEventListener('click', () => chooseSingle(selectionJobId, node.item.id, button));
    const meta = document.createElement('span');
    meta.className = 'tree-file-meta';
    meta.textContent = formatBytes(node.item.size);
    label.innerHTML = '<svg class="ui-icon tree-file-icon"><use href="#icon-file"></use></svg>';
    const name = document.createElement('span');
    name.className = 'tree-file-name';
    name.textContent = node.name;
    label.appendChild(name);
    row.appendChild(label);
    row.appendChild(meta);
    row.appendChild(button);
    return row;
  }

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
  generateButton.disabled = count === 0;
}

async function onGenerate() {
  const ids = selectedItemIds();
  if (!ids.length) return;
  generateButton.disabled = true;
  setButtonLabel(generateButton, '生成中...');
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
    setButtonLabel(generateButton, '生成下载链接');
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
// proxy link. The mode the user picked decides which link is emphasized, but
// both are shown when available.
function renderResults(job, results) {
  resultPanel.classList.remove('hidden');
  resultCount.textContent = `${results.length} 个文件`;
  resultList.innerHTML = '';

  for (const result of results) {
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

    if (result.direct_url) {
      card.appendChild(buildLinkRow('直链', 'direct', result.direct_url));
    }
    if (result.proxy_url) {
      card.appendChild(buildLinkRow('代理链接', 'proxy', result.proxy_url));
    }
    resultList.appendChild(card);
  }

  requestAnimationFrame(() => resultPanel.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
}

function buildLinkRow(tagText, tagClass, url) {
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
  selectionJobId = null;
  selectionStage = '';
  checkboxByItemId = new Map();
  hide(jobError);
  selectionPanel.classList.add('hidden');
  selectionTree.innerHTML = '';
  resultPanel.classList.add('hidden');
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
