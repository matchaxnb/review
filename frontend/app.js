// Code Review Tool - Main Application
(function() {
  'use strict';

  const state = {
    tree: [],
    openDirs: {},
    currentFile: null,
    language: '',
    annotations: {},      // end line → {startLine, endLine, outdated, comments:[{id, replyTo, comment, suggestion}]} for the current file
    allAnnotations: {},   // path → the same, for every file
    diffLines: {},        // line → "added"|"modified" for current file
    diffHunks: [],        // [{startLine, endLine, diff}] for current file
    diffDeletions: [],    // [{afterLine, hunkIndex}] for current file
    totalLines: 0,        // number of lines in the current file
    scrollPositions: {},  // path → scrollTop of the code view when the file was left
    editingLine: null,    // end line of the thread being commented on (the thread's key)
    replyTo: null,        // id of the comment being answered, null for a new comment
    selectionStart: null, // first line of the range being selected/edited
    selectionEnd: null,   // last line of the range being selected/edited
    anchorLine: null,     // line a shift-click extends the selection from
    activeThread: null,   // end line of the thread shown in the panel, for its highlight
    showSuggestion: false,// whether the suggested-change field is open in the editor
    gitStatuses: {},
    wsConnected: false,
    // History review: commits from the base to HEAD (newest first), the
    // selected commit's hash ('' = working tree), and a per-rev cache of tree
    // and statuses so stepping back and forth is instant.
    commits: [],
    rev: '',
    revCache: {},
    historyEnabled: false, // retired reviews are kept rather than deleted
    historyDest: '',       // where the current review would be moved on the next one
    baseShort: '',        // the base revision abbreviated, as git prints it
    sideBySide: true,      // show diff hunks as two columns (the server can turn this off)
  };

  let ws = null;
  let wsReconnectDelay = 1000;
  let shuttingDown = false;

  // DOM references
  let treePane, treeContainer, codeContent, codeHeader, commentList, commentEditor,
      editorTextarea, editorLineLabel, statusCommentCount, statusMdPath,
      statusBase, wsIndicator, toastContainer,
      commitBar, commitSelect, commitPrev, commitNext, commitSubject,
      suggestionRow, suggestionTextarea, suggestionBtn, cancelReplyBtn, saveBtn;

  // Initialize
  document.addEventListener('DOMContentLoaded', () => {
    treePane = document.querySelector('.file-tree');
    treeContainer = document.getElementById('tree-container');
    codeContent = document.getElementById('code-content');
    codeHeader = document.getElementById('code-header');
    commentList = document.getElementById('comment-list');
    commentEditor = document.getElementById('comment-editor');
    editorTextarea = document.getElementById('editor-textarea');
    editorLineLabel = document.getElementById('editor-line-label');
    suggestionRow = document.getElementById('editor-suggestion');
    suggestionTextarea = document.getElementById('editor-suggestion-textarea');
    suggestionBtn = document.getElementById('editor-suggestion-btn');
    cancelReplyBtn = document.getElementById('editor-cancel-reply');
    saveBtn = document.getElementById('editor-save');
    statusCommentCount = document.getElementById('status-comment-count');
    statusMdPath = document.getElementById('status-md-path');
    statusBase = document.getElementById('status-base');
    wsIndicator = document.getElementById('ws-indicator');
    toastContainer = document.getElementById('toast-container');
    commitBar = document.getElementById('commit-bar');
    commitSelect = document.getElementById('commit-select');
    commitPrev = document.getElementById('commit-prev');
    commitNext = document.getElementById('commit-next');
    commitSubject = document.getElementById('commit-subject');

    attachCodeViewHandlers();
    attachCommitHandlers();

    loadConfig();
    loadCommits();
    loadTree();
    loadAllAnnotations();
    connectWebSocket();

    // Reposition scrollbar markers when code-content resizes
    new ResizeObserver(() => {
      const strip = document.querySelector('.scrollbar-markers');
      if (strip) positionScrollbarStrip(strip);
    }).observe(codeContent);
  });

  // API helpers
  // withRev appends the currently selected revision to an API path, so tree,
  // statuses and file content are all read at the same commit.
  function withRev(path) {
    if (!state.rev) return path;
    return path + (path.includes('?') ? '&' : '?') + 'rev=' + encodeURIComponent(state.rev);
  }

  async function api(method, path, body) {
    const opts = { method, headers: {} };
    if (body) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(path, opts);
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(err.error || 'Request failed');
    }
    return res.json();
  }

  // WebSocket connection
  function connectWebSocket() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/ws');

    ws.onopen = () => {
      state.wsConnected = true;
      wsReconnectDelay = 1000;
      updateWsIndicator();
    };

    ws.onclose = () => {
      // The server said it was going away, so there is nothing to reconnect to
      if (shuttingDown) return;

      state.wsConnected = false;
      updateWsIndicator();
      // Reconnect with exponential backoff
      setTimeout(() => {
        wsReconnectDelay = Math.min(wsReconnectDelay * 2, 30000);
        connectWebSocket();
      }, wsReconnectDelay);
    };

    ws.onerror = () => {
      ws.close();
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        handleWsMessage(msg);
      } catch (e) {
        console.error('Failed to parse WebSocket message:', e);
      }
    };
  }

  function handleWsMessage(msg) {
    switch (msg.type) {
      case 'file-changed':
        // Update allAnnotations for this file
        if (msg.annotations) {
          state.allAnnotations[msg.path] = msg.annotations;
        }
        // The file being viewed reloads, which is feedback enough. Any other
        // file only shows up as a notice.
        if (state.currentFile === msg.path) {
          refreshCurrentFile(msg.annotations);
        } else {
          showToast('File changed: ' + msg.path);
        }
        updateCommentCount();
        renderTree();
        loadTreeSoon(); // the change may show up as a new git status
        break;

      case 'review-deleted':
        showToast('REVIEW.md was deleted — all annotations lost', true);
        state.allAnnotations = {};
        state.annotations = {};
        updateCommentCount();
        renderCommentList();
        renderTree();
        if (state.currentFile) {
          refreshCurrentFile();
        }
        break;

      case 'source-changed':
        // Source file changed on disk — reload if currently viewing it
        if (state.currentFile === msg.path) {
          refreshCurrentFile();
        }
        loadTreeSoon(); // the change may show up as a new git status
        break;

      case 'tree-changed':
        // A file appeared or went away
        loadTreeSoon();
        break;

      case 'review-reloaded': {
        showToast('REVIEW.md reloaded from disk');
        let current;
        if (msg.allAnnotations) {
          state.allAnnotations = msg.allAnnotations;
          current = msg.allAnnotations[state.currentFile] || {};
        } else {
          loadAllAnnotations();
        }
        if (state.currentFile) {
          refreshCurrentFile(current);
        }
        updateCommentCount();
        renderTree();
        break;
      }

      case 'server-shutdown':
        // Server is shutting down — close the tab
        shuttingDown = true;
        document.title = 'Review — closed';
        window.close();
        // Fallback if window.close() is blocked by browser policy
        document.body.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100vh;color:var(--rv-muted)"><p>Server stopped. You can close this tab.</p></div>';
        break;
    }
  }

  function updateWsIndicator() {
    if (!wsIndicator) return;
    wsIndicator.className = 'ws-indicator ' + (state.wsConnected ? 'connected' : 'disconnected');
    wsIndicator.title = state.wsConnected ? 'Live updates connected' : 'Live updates disconnected';
  }

  function showToast(message, isError) {
    if (!toastContainer) return;
    const toast = document.createElement('div');
    toast.className = 'toast' + (isError ? ' toast-error' : '');
    toast.textContent = message;
    toastContainer.appendChild(toast);
    setTimeout(() => {
      toast.classList.add('toast-fade');
      setTimeout(() => toast.remove(), 300);
    }, 4000);
  }

  // Load review settings and show the compare base if one is set
  async function loadConfig() {
    try {
      const config = await api('GET', '/api/config');
      state.baseRev = config.base || '';
      applyConfig(config);
    } catch (e) {
      console.error('Failed to load config:', e);
    }
  }

  // Load the commit history (base..HEAD, newest first) and build the selector.
  // Entry 0 is the aggregate view — the base diffed against the working tree,
  // i.e. one net diff over every commit in the range (what you want when a
  // change is made and reverted across commits) — and the remaining entries are
  // the individual commits, newest first, for optional drill-down. History
  // review only makes sense against a base ref, so the bar stays hidden without
  // one.
  async function loadCommits() {
    let base = '';
    try {
      const data = await api('GET', '/api/commits');
      state.commits = data.commits || [];
      base = data.base || '';
    } catch (e) {
      console.error('Failed to load commits:', e);
      state.commits = [];
    }
    if (!base) {
      // No base range: history review is not applicable, hide the bar.
      state.commits = [];
      commitBar.style.display = 'none';
      return;
    }
    renderCommitBar();
  }

  // aggregateLabel describes the default view, which diffs the base commit
  // against the working tree: one net diff over every commit in the range
  // (plus uncommitted work), so a change made and reverted is not shown.
  function aggregateLabel() {
    const n = state.commits.length;
    if (!n) return 'all changes since ' + baseLabel();
    return 'all ' + n + ' commit' + (n === 1 ? '' : 's') +
           ' since ' + baseLabel() + ' (net diff)';
  }

  // baseLabel names the base revision for the commit bar and the status bar:
  // the short hash when one was resolved, since that is what identifies the
  // point in history, and what the user typed otherwise.
  function baseLabel() {
    return state.baseShort || shortRev(state.baseRev) || 'HEAD';
  }

  function renderCommitBar() {
    commitSelect.innerHTML = '';
    const wt = document.createElement('option');
    wt.value = '';
    wt.textContent = 'All changes (' + baseLabel() + ' → working tree)';
    commitSelect.appendChild(wt);
    state.commits.forEach((c, i) => {
      const o = document.createElement('option');
      o.value = c.hash;
      o.textContent = c.short + '  ' + c.subject;
      commitSelect.appendChild(o);
    });
    commitSelect.value = state.rev || '';
    updateCommitNav();
  }

  function commitIndex() {
    if (!state.rev) return 0;
    const i = state.commits.findIndex(c => c.hash === state.rev);
    return i < 0 ? -1 : i + 1; // +1 for the working-tree entry at index 0
  }

  function updateCommitNav() {
    const i = commitIndex();
    commitPrev.disabled = i <= 0;                 // toward HEAD
    commitNext.disabled = i < 0 || i >= state.commits.length; // older
    const c = i > 0 ? state.commits[i - 1] : null;
    commitSubject.textContent = c ? c.subject : aggregateLabel();
    if (statusBase) {
      statusBase.style.display = '';
      statusBase.textContent = c ? ('reviewing ' + c.short)
                                 : 'compared to ' + baseLabel();
    }
  }

  function attachCommitHandlers() {
    if (!commitBar) return;
    commitSelect.addEventListener('change', () => selectCommit(commitSelect.value));
    commitPrev.addEventListener('click', () => stepCommit(-1));
    commitNext.addEventListener('click', () => stepCommit(1));
  }

  function stepCommit(delta) {
    const i = commitIndex();
    if (i < 0) return;
    const ni = i + delta;
    if (ni < 0 || ni > state.commits.length) return;
    selectCommit(ni === 0 ? '' : state.commits[ni - 1].hash);
  }

  // Switch the entire view to a commit ('' = working tree). Committed views are
  // cached per rev so stepping back and forth is instant; working-tree views
  // are always refetched since they change on disk.
  async function selectCommit(rev) {
    state.rev = rev || '';
    commitSelect.value = state.rev;
    updateCommitNav();
    state.currentFile = null;
    state.scrollPositions = {};
    hideDiffTooltip(); // it was drawn from the revision being left
    if (state.rev && state.revCache[state.rev]) {
      const c = state.revCache[state.rev];
      state.tree = c.tree;
      state.gitStatuses = c.gitStatuses;
      renderTree();
    } else {
      await loadTree();
      if (state.rev) state.revCache[state.rev] = { tree: state.tree, gitStatuses: state.gitStatuses };
    }
    codeHeader.innerHTML = '<span class="file-path">Select a file to review</span>';
    codeContent.innerHTML = '<div class="empty-state"><p>Select a file from the tree to review.</p></div>';
  }

  // Apply the review settings the config endpoint returned.
  function applyConfig(config) {
    state.historyEnabled = !!config.historyEnabled;
    state.historyDest = config.historyDest || '';
    state.sideBySide = config.sideBySide !== false;
    state.baseRev = config.base || '';
    state.baseShort = config.baseShort || '';
    if (config.base && statusBase) {
      // The status bar names the base by the revision the user gave and, when
      // that is a hash, the short form alongside it: the revision is what was
      // typed, the hash is what the diffs were taken from.
      const short = config.baseShort && config.baseShort !== config.base
        ? ' (' + config.baseShort + ')'
        : '';
      statusBase.textContent = 'compared to ' + config.base + short;
      statusBase.title = 'Changes are highlighted relative to ' + config.base;
      statusBase.style.display = '';
    }
  }

  // Load the file tree together with the git status it is coloured by, so the
  // two are always drawn from the same view of the project.
  async function loadTree() {
    try {
      const [tree, gitStatuses] = await Promise.all([
        api('GET', withRev('/api/tree')),
        api('GET', withRev('/api/git-status')),
      ]);
      state.tree = tree;
      state.gitStatuses = gitStatuses;
      renderTree();
    } catch (e) {
      console.error('Failed to load tree:', e);
    }
  }

  // Reload the tree after a change on disk, debounced so that a burst of
  // changes does not run git once per file.
  let treeReloadTimer = null;
  function loadTreeSoon() {
    if (treeReloadTimer) clearTimeout(treeReloadTimer);
    treeReloadTimer = setTimeout(loadTree, 500);
  }

  // Load all annotations (for gutter dots and comment counts)
  async function loadAllAnnotations() {
    try {
      state.allAnnotations = await api('GET', '/api/annotations');
      updateCommentCount();
    } catch (e) {
      console.error('Failed to load annotations:', e);
    }
  }

  // Get git status for a file path
  function getGitStatus(path) {
    return state.gitStatuses[path] || '';
  }

  // How much a git status stands out, used to pick what a directory shows
  const statusPriority = { conflict: 5, modified: 4, deleted: 4, untracked: 3, added: 2, staged: 1 };

  // What a directory row shows, by directory path. Collected in one pass per
  // render rather than walking every subtree again for each row.
  let dirSummaries = new Map();

  // summarize walks a subtree once, recording for every directory the most
  // important git status below it and whether it holds any comments.
  function summarize(entry) {
    if (!entry.isDir) {
      const anns = state.allAnnotations[entry.path];
      return { status: getGitStatus(entry.path), comments: !!(anns && Object.keys(anns).length > 0) };
    }

    let status = '';
    let comments = false;
    for (const child of entry.children || []) {
      const childSummary = summarize(child);
      if ((statusPriority[childSummary.status] || 0) > (statusPriority[status] || 0)) {
        status = childSummary.status;
      }
      comments = comments || childSummary.comments;
    }

    const summary = { status, comments };
    dirSummaries.set(entry.path, summary);
    return summary;
  }

  // The sprite the tree's icons come from
  const icons = '/static/assets/icons.svg';

  // The row of each rendered path, so that a single row can be reached without
  // searching the tree for it, and the row marked as the open file.
  let treeRows = new Map();
  let activeRow = null;

  // Render file tree
  function renderTree() {
    dirSummaries = new Map();
    state.tree.forEach(summarize);

    const scrollTop = treePane.scrollTop;
    treeRows = new Map();
    activeRow = null;
    treeContainer.innerHTML = '';
    renderTreeLevel(state.tree, treeContainer, 0);
    treePane.scrollTop = scrollTop;
  }

  // Move the marking for the open file, which is all that opening one changes
  // about the tree.
  function setActiveFile(path) {
    if (activeRow) activeRow.classList.remove('active');
    activeRow = treeRows.get(path) || null;
    if (activeRow) activeRow.classList.add('active');
  }

  function renderTreeLevel(entries, container, depth) {
    entries.forEach(entry => {
      const item = document.createElement('div');
      item.className = 'tree-item';
      item.style.paddingLeft = (0.5 + depth * 1) + 'rem';
      treeRows.set(entry.path, item);

      if (entry.isDir) {
        const isOpen = state.openDirs[entry.path] || false;
        const summary = dirSummaries.get(entry.path) || { status: '', comments: false };
        if (summary.status) item.classList.add('git-' + summary.status);

        item.innerHTML =
          `<svg class="chevron${isOpen ? ' open' : ''}"><use href="${icons}#icon-chevron-right"/></svg>` +
          `<svg><use href="${icons}#icon-folder${isOpen ? '-open' : ''}"/></svg>` +
          `<span>${escapeHtml(entry.name)}</span>` +
          (summary.comments ? '<span class="comment-dot"></span>' : '');

        item.addEventListener('click', () => {
          state.openDirs[entry.path] = !state.openDirs[entry.path];
          renderTree();
        });

        container.appendChild(item);

        if (entry.children && isOpen) {
          const childContainer = document.createElement('div');
          childContainer.className = 'tree-children open';
          renderTreeLevel(entry.children, childContainer, depth + 1);
          container.appendChild(childContainer);
        }
      } else {
        const fileAnns = state.allAnnotations[entry.path];
        const hasComments = fileAnns && Object.keys(fileAnns).length > 0;
        const hasOutdated = hasComments && Object.values(fileAnns).some(a => a.outdated);
        const fileStatus = getGitStatus(entry.path);
        if (fileStatus) item.classList.add('git-' + fileStatus);

        item.innerHTML =
          `<svg><use href="${icons}#icon-file"/></svg>` +
          `<span>${escapeHtml(entry.name)}</span>` +
          (hasOutdated ? '<span class="comment-dot outdated-dot"></span>'
            : hasComments ? '<span class="comment-dot"></span>' : '');

        if (state.currentFile === entry.path) {
          item.classList.add('active');
          activeRow = item;
        }

        item.addEventListener('click', () => openFile(entry.path));
        container.appendChild(item);
      }
    });
  }

  // Load the open file's content and annotations into the code view. The view
  // ends up at scrollTop, which defaults to where it stands now so that a
  // reload after a change stays where you were.
  async function loadCurrentFile(annotations, scrollTop = codeContent.scrollTop) {
    const path = state.currentFile;
    if (!path) return;

    // Whatever hunk is on screen belongs to the file being replaced, so it
    // goes first: the diff data it was drawn from is about to be replaced too.
    hideDiffTooltip();

    const [fileData, annData] = await Promise.all([
      api('GET', withRev('/api/file?path=' + encodeURIComponent(path))),
      annotations || api('GET', '/api/annotations?path=' + encodeURIComponent(path)),
    ]);
    // Another file was opened while this one was loading
    if (state.currentFile !== path) return;

    state.language = fileData.language;
    state.diffLines = fileData.diffLines || {};
    state.diffHunks = fileData.diffHunks || [];
    state.diffDeletions = fileData.diffDeletions || [];
    state.annotations = annData;

    codeHeader.innerHTML = `
      <span class="file-path">${escapeHtml(path)}</span>
      <span class="lang-badge">${escapeHtml(state.language)}</span>
    `;
    codeContent.innerHTML = fileData.html;
    codeContent.scrollTop = scrollTop;
    renderCodeView();
    renderCommentList();
  }

  // Reload the open file after it changed on disk. Annotations that came with
  // the change are passed on rather than asked for a second time.
  async function refreshCurrentFile(annotations) {
    try {
      await loadCurrentFile(annotations);
    } catch (e) {
      console.error('Failed to refresh file:', e);
    }
  }

  // Open a file from the tree. Where the file being left stands is noted, and
  // the file being opened returns to where it was last left.
  async function openFile(path) {
    if (state.currentFile) {
      state.scrollPositions[state.currentFile] = codeContent.scrollTop;
    }
    state.currentFile = path;
    closeEditor();

    // Tell server to watch this file for changes
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'watch-file', path: path }));
    }

    codeHeader.innerHTML = `
      <span class="file-path">${escapeHtml(path)}</span>
      <span class="lang-badge">loading...</span>
    `;
    codeContent.innerHTML = '<div class="loading"><div class="spinner"></div> Loading...</div>';
    setActiveFile(path);

    try {
      await loadCurrentFile(null, state.scrollPositions[path] || 0);
    } catch (e) {
      codeContent.innerHTML = `<div class="empty-state"><p>Error loading file: ${escapeHtml(e.message)}</p></div>`;
    }
  }

  // The gutter cell of a line carries the id chroma generated for it, which
  // makes looking a line up a hash lookup rather than a walk over the file.
  function lineElement(lineNum) {
    const gutter = document.getElementById('L' + lineNum);
    return gutter ? gutter.parentElement : null;
  }

  // The line number an element sits on, or null for the deletion markers, which
  // stand between lines and have no number of their own.
  function lineNumberOf(lineEl) {
    const gutter = lineEl.firstElementChild;
    if (!gutter || !gutter.id) return null;
    const lineNum = parseInt(gutter.id.slice(1), 10);
    return Number.isInteger(lineNum) ? lineNum : null;
  }

  // The diff hunk a line belongs to, or null. Hunks arrive in order, so this
  // finds one without looking at every hunk.
  function hunkAt(lineNum) {
    let low = 0;
    let high = state.diffHunks.length - 1;
    while (low <= high) {
      const mid = (low + high) >> 1;
      const hunk = state.diffHunks[mid];
      if (lineNum < hunk.startLine) high = mid - 1;
      else if (lineNum > hunk.endLine) low = mid + 1;
      else return hunk;
    }
    return null;
  }

  // Handle clicks and gutter hovers for the whole code view. Registered once for
  // the container rather than per line, of which a large file holds tens of
  // thousands.
  function attachCodeViewHandlers() {
    codeContent.addEventListener('click', (e) => {
      const lineEl = e.target.closest('.line');
      if (!lineEl) return;
      // A drag that highlighted several lines is the range the user means; the
      // click that ends it must not shrink the comment back to one line.
      const hl = highlightedLineRange();
      if (hl && hl[1] > hl[0]) {
        state.anchorLine = hl[0];
        beginComment(hl[0], hl[1], true);
        return;
      }
      const lineNum = lineNumberOf(lineEl);
      if (lineNum !== null) clickLine(lineNum, e.shiftKey);
    });

    codeContent.addEventListener('mouseover', (e) => {
      const gutter = e.target.closest('.ln');
      if (!gutter) return;

      // A deletion marker names its hunk; a line is looked up by its number
      let hunk = null;
      if (gutter.dataset.hunkIndex !== undefined) {
        hunk = state.diffHunks[Number(gutter.dataset.hunkIndex)] || null;
      } else {
        const lineNum = lineNumberOf(gutter.parentElement);
        if (lineNum !== null && state.diffLines[lineNum]) hunk = hunkAt(lineNum);
      }
      if (hunk) showDiffTooltip(e, hunk);
    });
  }

  // Draw the diff and comment markers over the file just loaded. Only the lines
  // that carry one are touched.
  function renderCodeView() {
    // Counted before the deletion markers are inserted: those carry the same
    // class but stand between lines rather than being ones, and counting them
    // would push every scrollbar marker up the strip.
    state.totalLines = codeContent.querySelectorAll('.chroma .line').length;

    for (const [lineNum, type] of Object.entries(state.diffLines)) {
      const lineEl = lineElement(lineNum);
      if (lineEl) lineEl.classList.add('diff-' + type);
    }

    for (const [lineNum, ann] of Object.entries(state.annotations)) {
      const { start, end } = annotationRange(ann, Number(lineNum));
      for (let n = start; n <= end; n++) {
        const lineEl = lineElement(n);
        if (!lineEl) continue;
        lineEl.classList.add('has-comment');
        if (ann.outdated) lineEl.classList.add('has-outdated-comment');
      }
      // A marker at the end of the thread's run says where to click to reopen
      // the comment: the gutter dots already mark every covered line, but they
      // are drawn by CSS and cannot carry the count or take a click.
      placeCommentMarker(end);
    }

    // Keep the lines being commented on marked after a redraw
    if (state.selectionStart && state.selectionEnd) {
      selectRange(state.selectionStart, state.selectionEnd);
    }

    renderDeletionMarkers();
    renderScrollbarMarkers();
  }

  // The marker standing at the end of a commented run, in the code view's own
  // frame. Clicking it opens that thread for reading and replying, which is the
  // affordance the gutter dot cannot give: a dot says a comment exists, this
  // says where to click to see it.
  function commentMarker(ann, lineNum) {
    const marker = document.createElement('button');
    marker.className = 'comment-marker';
    marker.type = 'button';
    marker.dataset.commentMarker = lineNum;
    const count = (ann.comments || []).length;
    marker.title = count > 1
      ? count + ' comments — click to open'
      : 'Comment — click to open';
    if (ann.outdated) marker.classList.add('comment-marker-outdated');
    marker.innerHTML = `<svg><use href="/static/assets/icons.svg#icon-comment"/></svg>` +
      (count > 1 ? `<span class="comment-marker-count">${count}</span>` : '');
    marker.addEventListener('click', (e) => {
      e.stopPropagation();
      openThread(lineNum);
    });
    return marker;
  }

  // Open a thread for reading and replying: the code view scrolls to it, the run
  // is marked, and the editor is armed to add a comment on those same lines. The
  // textarea starts empty — comments are only ever appended, so opening a thread
  // never puts an existing comment in it to be rewritten.
  function openThread(lineNum) {
    const ann = threadAt(lineNum);
    if (!ann) return;
    const { start, end } = annotationRange(ann, ann.endLine);
    state.activeThread = ann.endLine;
    // The run is marked and the editor armed for the thread, so a comment can
    // be added where the one being read was made.
    openThreadEditor(start, end, ann, null, false);
    selectRange(start, end);
    scrollToLine(end);
  }

  // Put the marker for one thread on its line, replacing any that is there. Used
  // by the render pass and by a save, which updates the open file in place
  // rather than redrawing it.
  function placeCommentMarker(lineNum) {
    const ann = state.annotations[lineNum];
    const lineEl = lineElement(lineNum);
    if (!lineEl) return;
    const old = lineEl.querySelector('.comment-marker');
    if (old) old.remove();
    if (ann) lineEl.appendChild(commentMarker(ann, lineNum));
  }

  // Take the comment markers off every line of the open file, for when the
  // annotations go away without the file being drawn again.
  function clearCommentMarkers() {
    codeContent.querySelectorAll('.has-comment').forEach(lineEl => {
      lineEl.classList.remove('has-comment', 'has-outdated-comment');
    });
  }

  // Insert a marker for each block of lines the diff removed.
  function renderDeletionMarkers() {
    state.diffDeletions.forEach(del => {
      const marker = document.createElement('span');
      marker.className = 'line diff-deleted-marker';

      const gutter = document.createElement('span');
      gutter.className = 'ln diff-del-gutter';
      gutter.textContent = '\u00a0'; // non-breaking space
      gutter.dataset.hunkIndex = del.hunkIndex;
      marker.appendChild(gutter);

      if (del.afterLine === 0) {
        // Deletion at top of file — insert before first line
        const firstLine = codeContent.querySelector('.chroma .line');
        if (firstLine) firstLine.before(marker);
      } else {
        const afterEl = lineElement(del.afterLine);
        if (afterEl) afterEl.after(marker);
      }
    });
  }

  // Render scrollbar markers for comments and diff lines
  function renderScrollbarMarkers() {
    let strip = document.querySelector('.scrollbar-markers');
    if (strip) strip.remove();

    const totalLines = state.totalLines;
    if (totalLines === 0) return;

    const markers = [];
    for (const [line, type] of Object.entries(state.diffLines)) {
      markers.push({ line: Number(line), color: type === 'added' ? '#16a34a' : '#d97706' });
    }
    for (const del of state.diffDeletions) {
      markers.push({ line: del.afterLine || 1, color: '#dc2626' });
    }
    for (const [line, ann] of Object.entries(state.annotations)) {
      // An outdated thread is marked in the same amber as an outdated line in
      // the tree, so the strip and the tree agree about what needs attention.
      markers.push({ line: Number(line), color: ann && ann.outdated ? '#d97706' : 'rgb(37, 99, 235)' });
    }
    if (markers.length === 0) return;

    strip = document.createElement('div');
    strip.className = 'scrollbar-markers';

    positionScrollbarStrip(strip);

    markers.forEach(m => {
      const mark = document.createElement('div');
      mark.className = 'scrollbar-mark';
      mark.style.top = ((m.line - 1) / totalLines * 100) + '%';
      mark.style.backgroundColor = m.color;
      strip.appendChild(mark);
    });
    document.body.appendChild(strip);
  }

  function positionScrollbarStrip(strip) {
    const rect = codeContent.getBoundingClientRect();
    strip.style.top = rect.top + 'px';
    strip.style.right = (window.innerWidth - rect.right) + 'px';
    strip.style.height = rect.height + 'px';
  }


  // The lines a comment covers, in order. The annotation's key is its end line;
  // startLine is stored alongside it. A comment written before ranges existed
  // has no start line and covers the end line alone.
  function annotationRange(ann, endLine) {
    const end = ann && ann.endLine ? ann.endLine : endLine;
    let start = ann && ann.startLine ? ann.startLine : end;
    if (start < 1 || start > end) start = end;
    return { start, end };
  }

  // The lines currently marked. Remembered so they can be cleared without
  // walking the file.
  let selectedLineEls = [];

  // Mark a run of lines as the one being commented on.
  function selectRange(start, end) {
    selectedLineEls.forEach(el => el.classList.remove('selected'));
    selectedLineEls = [];
    for (let n = start; n <= end; n++) {
      const el = lineElement(n);
      if (el) {
        el.classList.add('selected');
        selectedLineEls.push(el);
      }
    }
  }

  // Arm the editor for a comment on a run of lines, from a click or a drag.
  // A highlight that ends on lines an existing thread covers opens that thread.
  function beginComment(start, end, focusText) {
    const ann = threadAt(end) || threadAt(start);
    if (ann) {
      const r = annotationRange(ann, ann.endLine);
      state.replyTo = null;
      openThreadEditor(r.start, r.end, ann, null, focusText);
      return;
    }
    state.replyTo = null;
    openThreadEditor(start, end, null, null, focusText);
  }

  // The thread covering a line, or null. It is keyed by its end line, so a line
  // inside a commented run is found by walking the threads and asking which of
  // them spans it — a click on any line of a range must reopen that range's
  // comment, not start an empty one on the line clicked.
  function threadAt(lineNum) {
    if (state.annotations[lineNum]) return state.annotations[lineNum];
    for (const ann of Object.values(state.annotations)) {
      const { start, end } = annotationRange(ann, ann.endLine);
      if (ann.endLine && lineNum >= start && lineNum <= end) return ann;
    }
    return null;
  }

  // The end line that keys the thread covering a line, or null.
  function threadKeyAt(lineNum) {
    const ann = threadAt(lineNum);
    return ann ? ann.endLine : null;
  }

  // Click a line to comment on it, extending it into a range when shift is held.
  // A plain click on a line an existing thread covers opens that thread — the
  // whole run it spans, with every comment on it — rather than a new one.
  function clickLine(lineNum, shiftKey) {
    let start, end;
    if (shiftKey && state.anchorLine) {
      start = Math.min(state.anchorLine, lineNum);
      end = Math.max(state.anchorLine, lineNum);
      state.editingLine = end;
      state.replyTo = null;
      openThreadEditor(start, end, null);
      return;
    }

    const ann = threadAt(lineNum);
    if (ann) {
      const { start: s, end: e } = annotationRange(ann, ann.endLine);
      state.anchorLine = lineNum;
      state.replyTo = null;
      openThreadEditor(s, e, ann);
      return;
    }

    state.anchorLine = lineNum;
    state.replyTo = null;
    openThreadEditor(lineNum, lineNum, null);
  }

  // Arm the editor for a new comment on a run of lines. replyingTo names the
  // comment being answered, when the comment is a reply. The textarea starts
  // empty either way: comments are only ever appended, so an existing comment is
  // never loaded into it for editing.
  function openThreadEditor(start, end, existing, replyingTo, focusText) {
    state.selectionStart = start;
    state.selectionEnd = end;
    state.editingLine = end; // a thread is keyed by its end line
    state.replyTo = replyingTo || null;
    state.activeThread = existing ? existing.endLine : null;
    selectRange(start, end);

    editorLineLabel.textContent = start === end ? 'Line ' + end : 'Lines ' + start + '\u2013' + end;
    editorTextarea.value = '';
    editorTextarea.placeholder = replyingTo
      ? 'Write your reply...'
      : (existing ? 'Add another comment...' : 'Write your review comment...');
    updateEditorVisibility();
    updateThreadActions(existing);
    renderCommentList();
    if (focusText) editorTextarea.focus();
  }

  // Show or hide the suggestion fields for the comment being written, and the
  // Cancel-reply control. The suggestion is offered for a new comment on a
  // range, which is where a replacement makes sense.
  function updateThreadActions(existing) {
    if (suggestionRow) suggestionRow.style.display = state.showSuggestion ? '' : 'none';
    if (cancelReplyBtn) cancelReplyBtn.style.display = state.replyTo ? '' : 'none';
    if (saveBtn) saveBtn.textContent = state.replyTo ? 'Reply' : 'Comment';
  }

  // The line a DOM node sits on, or null when it is not inside a line.
  function lineOfNode(node) {
    if (!node) return null;
    const el = node.nodeType === 1 ? node : node.parentElement;
    const lineEl = el && el.closest && el.closest('.line');
    return lineEl ? lineNumberOf(lineEl) : null;
  }

  // The line numbers a highlight in the code view covers, or null when there is
  // no such highlight. Both ends are read off the selected nodes, which is a
  // lookup each; only a selection whose ends fall outside the lines falls back
  // to measuring rectangles.
  function highlightedLineRange() {
    const sel = document.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
    if (!codeContent.contains(sel.anchorNode) || !codeContent.contains(sel.focusNode)) return null;

    const a = lineOfNode(sel.anchorNode);
    const b = lineOfNode(sel.focusNode);
    if (a !== null && b !== null) return [Math.min(a, b), Math.max(a, b)];

    const rects = Array.from(sel.getRangeAt(0).getClientRects());
    if (rects.length === 0) return null;
    const top = Math.min(...rects.map(r => r.top));
    const bottom = Math.max(...rects.map(r => r.bottom));
    let min = Infinity;
    let max = -Infinity;
    codeContent.querySelectorAll('.chroma .line').forEach(el => {
      const r = el.getBoundingClientRect();
      if (r.bottom <= top || r.top >= bottom) return;
      const n = lineNumberOf(el);
      if (n === null) return;
      if (n < min) min = n;
      if (n > max) max = n;
    });
    return min === Infinity ? null : [min, max];
  }

  // A highlight of one or more whole lines becomes the comment's range, so the
  // review covers exactly what was selected.
  let highlightSyncFrame = null;
  function syncCommentToHighlight() {
    if (!state.currentFile) return;
    const range = highlightedLineRange();
    if (!range) return;
    // Only react when the highlight actually moved, or every caret movement
    // inside the textarea would reopen the same range.
    if (range[0] === state.selectionStart && range[1] === state.selectionEnd) return;
    state.anchorLine = range[0];
    beginComment(range[0], range[1], false);
  }

  document.addEventListener('selectionchange', () => {
    if (highlightSyncFrame) return;
    highlightSyncFrame = requestAnimationFrame(() => {
      highlightSyncFrame = null;
      syncCommentToHighlight();
    });
  });

  // Save a comment, or a reply. A comment on more than one line is sent as a
  // Gerrit-style range: start_line and end_line, with the end line also as the
  // plain line. Nothing is ever edited in place: a second comment on the same
  // lines is appended to the thread, and a reply names the comment it answers.
  async function saveComment() {
    if (!state.currentFile || !state.editingLine) return;
    const text = editorTextarea.value.trim();
    const suggestion = suggestionTextarea ? suggestionTextarea.value.replace(/\s+$/, '') : '';
    if (!text && !suggestion) {
      showToast('Write a comment or a suggested change first', true);
      return;
    }

    const start = state.selectionStart || state.editingLine;
    const end = state.editingLine;
    const body = { path: state.currentFile, line: end, comment: text };
    if (suggestion) body.suggestion = suggestion;
    if (state.replyTo) body.replyTo = state.replyTo;
    if (start !== end) {
      body.range = { start_line: start, end_line: end };
    }

    try {
      const result = await api('POST', '/api/annotations', body);
      const ann = state.annotations[end];
      if (ann) {
        ann.comments = ann.comments || [];
        ann.comments.push({
          id: result.id,
          replyTo: state.replyTo || 0,
          comment: text,
          suggestion,
        });
      } else {
        state.annotations[end] = {
          startLine: start,
          endLine: end,
          outdated: false,
          comments: [{ id: result.id, replyTo: state.replyTo || 0, comment: text, suggestion }],
        };
      }
      if (!state.allAnnotations[state.currentFile]) {
        state.allAnnotations[state.currentFile] = {};
      }
      state.allAnnotations[state.currentFile][end] = state.annotations[end];

      // Update gutter across the range, and the marker that reopens it
      for (let n = start; n <= end; n++) {
        const lineEl = lineElement(n);
        if (lineEl) {
          lineEl.classList.add('has-comment');
          lineEl.classList.remove('has-outdated-comment');
        }
      }
      placeCommentMarker(end);

      const keep = { start, end, line: end };
      annotationsChanged();
      // A reply leaves the thread open so the exchange can carry on; a new
      // comment closes the editor with it.
      state.replyTo = null;
      openThreadEditor(keep.start, keep.end, state.annotations[keep.end], null, false);
    } catch (e) {
      showToast('Failed to save: ' + e.message, true);
    }
  }

  // Answer a comment: the editor is armed for a reply to it, on its own lines.
  function replyToComment(lineNum, id) {
    const ann = state.annotations[lineNum];
    if (!ann) return;
    const { start, end } = annotationRange(ann, lineNum);
    openThreadEditor(start, end, ann, id, true);
    scrollToLine(end);
  }

  // Delete one comment, along with everything answering it — the server marks
  // them deleted, and they are written out of the file when the review is next
  // opened or closed. lineNum is the thread it sits in, which the panel knows
  // even when no thread is open in the editor.
  //
  // Replies go with the comment they answer, so a delete that would take any is
  // confirmed first: the count is what the reviewer is agreeing to.
  async function deleteComment(id, lineNum) {
    if (!state.currentFile || !id) return;

    const line = lineNum || state.activeThread || state.editingLine;
    if (!line) return;

    const thread = state.annotations[line];
    const replies = thread ? descendantCount(thread.comments || [], id) : 0;
    if (replies > 0) {
      const question = replies === 1
        ? 'Delete this comment and the reply to it?'
        : 'Delete this comment and the ' + replies + ' replies to it?';
      if (!confirm(question)) return;
    }

    try {
      await api('DELETE', '/api/annotations', { path: state.currentFile, id: id });

      if (thread) {
        thread.comments = (thread.comments || []).filter(c => c.id !== id && !answers(c, id, thread.comments));
        if (thread.comments.length === 0) {
          delete state.annotations[line];
          if (state.allAnnotations[state.currentFile]) {
            delete state.allAnnotations[state.currentFile][line];
            if (Object.keys(state.allAnnotations[state.currentFile]).length === 0) {
              delete state.allAnnotations[state.currentFile];
            }
          }
          clearThreadMarkers(annotationRange(thread, line));
          const openEl = lineElement(line);
          const old = openEl && openEl.querySelector('.comment-marker');
          if (old) old.remove();
        } else {
          state.allAnnotations[state.currentFile][line] = thread;
          placeCommentMarker(line);
        }
      }

      const wasActive = state.activeThread === line;
      annotationsChanged();
      if (state.annotations[line]) {
        const { start, end } = annotationRange(state.annotations[line], line);
        openThreadEditor(start, end, state.annotations[line], null, false);
      } else if (wasActive) {
        state.activeThread = null;
          }
    } catch (e) {
      showToast('Failed to delete: ' + e.message, true);
    }
  }

  // answers reports whether a comment, or anything answering it in turn, is the
  // one being deleted. The server deletes the whole subtree, so the client takes
  // the same set off the screen without asking for the threads again.
  function answers(c, id, thread) {
    for (let cur = c; cur && cur.replyTo; ) {
      if (cur.replyTo === id) return true;
      cur = thread.find(p => p.id === cur.replyTo);
    }
    return false;
  }

  // descendantCount counts the comments that answer the one with this id, at any
  // depth: the ones a delete takes with it. The walk is bounded by the number of
  // comments in the thread, so a malformed exchange cannot spin here.
  function descendantCount(thread, id) {
    let count = 0;
    for (const c of thread) {
      if (c.id !== id && answers(c, id, thread)) count++;
    }
    return count;
  }

  // Take the comment markers off a run of lines.
  function clearThreadMarkers({ start, end }) {
    for (let n = start; n <= end; n++) {
      const lineEl = lineElement(n);
      if (lineEl) lineEl.classList.remove('has-comment', 'has-outdated-comment');
    }
  }

  // Scroll a line into the middle of the code view.
  function scrollToLine(lineNum) {
    const lineEl = lineElement(lineNum);
    if (lineEl) lineEl.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  // Show the suggested-change field for the comment being written.
  function showSuggestion() {
    state.showSuggestion = true;
    updateThreadActions();
    if (suggestionTextarea) {
      suggestionTextarea.focus();
      prefillSuggestion();
    }
  }

  // Hide the suggested-change field, leaving any text in it alone so it is not
  // lost by a stray click on the toggle.
  function hideSuggestion() {
    state.showSuggestion = false;
    updateThreadActions();
  }

  function toggleSuggestion() {
    if (state.showSuggestion) hideSuggestion();
    else showSuggestion();
  }

  // Fill the suggestion field with the lines the comment covers, so a small edit
  // to them can be made rather than retyped. Line numbers are not included: a
  // suggestion is the code that should stand there.
  function prefillSuggestion() {
    if (!suggestionTextarea || suggestionTextarea.value.trim()) return;
    const lines = [];
    for (let n = state.selectionStart; n <= state.selectionEnd; n++) {
      const lineEl = lineElement(n);
      if (!lineEl) continue;
      const code = lineEl.querySelector('.cl');
      if (code) lines.push(code.textContent.replace(/\n$/, ''));
    }
    if (lines.length) suggestionTextarea.value = lines.join('\n');
    suggestionTextarea.selectionStart = 0;
    suggestionTextarea.selectionEnd = 0;
  }

  // Give up on a reply, leaving the thread open for a comment of its own.
  function cancelReply() {
    state.replyTo = null;
    updateThreadActions();
    editorTextarea.value = '';
    editorTextarea.placeholder = 'Add another comment...';
    editorTextarea.focus();
  }

  // Put the editor away, leaving no line marked as being commented on.
  function closeEditor() {
    state.editingLine = null;
    state.replyTo = null;
    state.selectionStart = null;
    state.selectionEnd = null;
    state.activeThread = null;
    state.showSuggestion = false;
    selectRange(0, -1);
    if (editorTextarea) editorTextarea.value = '';
    if (suggestionTextarea) suggestionTextarea.value = '';
    updateEditorVisibility();
    renderCommentList();
  }

  // Bring everything that shows annotations back in line after one changed. The
  // editor goes with it: the thread it was armed for may not be there any more,
  // and callers that keep it open reopen the thread themselves.
  function annotationsChanged() {
    closeEditor();
    renderScrollbarMarkers();
    renderTree();
    updateCommentCount();
  }

  // Render the threads of the open file in the sidebar, each comment numbered
  // and a reply indented under the comment it answers.
  function renderCommentList() {
    if (!commentList) return;

    const lines = Object.keys(state.annotations).map(Number).sort((a, b) => a - b);
    if (lines.length === 0) {
      commentList.innerHTML = '<div class="empty-state" style="padding:2rem"><small>No comments on this file.<br>Click a line to add one.</small></div>';
      return;
    }

    commentList.innerHTML = lines.map(lineNum => {
      const ann = state.annotations[lineNum];
      const { start, end } = annotationRange(ann, lineNum);
      const outdatedClass = ann && ann.outdated ? ' comment-outdated' : '';
      const outdatedBadge = ann && ann.outdated ? '<span class="outdated-badge">outdated</span>' : '';
      const label = start === end ? 'Line ' + end : 'Lines ' + start + '\u2013' + end;
      const active = state.activeThread === lineNum ? ' comment-item-active' : '';
      const comments = (ann && ann.comments) || [];
      return `
        <div class="comment-item${outdatedClass}${active}" data-comment-line="${lineNum}">
          <div class="comment-head">
            <span class="comment-line">${label}</span> ${outdatedBadge}
          </div>
          ${comments.map(c => commentHtml(c, comments, lineNum)).join('')}
        </div>
      `;
    }).join('');

    // Clicking a thread's heading opens it; clicking a comment's body does not,
    // so text can be selected without the editor taking over.
    commentList.querySelectorAll('.comment-item').forEach(item => {
      const ln = parseInt(item.dataset.commentLine, 10);
      item.querySelector('.comment-head').addEventListener('click', () => {
        clickLine(ln);
        scrollToLine(ln);
      });
    });
    commentList.querySelectorAll('[data-reply]').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        replyToComment(parseInt(btn.dataset.replyLine, 10), parseInt(btn.dataset.reply, 10));
      });
    });
    commentList.querySelectorAll('[data-delete]').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const id = parseInt(btn.dataset.delete, 10);
        deleteComment(id, parseInt(btn.dataset.deleteLine, 10));
      });
    });
    commentList.querySelectorAll('[data-open-line]').forEach(el => {
      el.addEventListener('click', () => {
        const ln = parseInt(el.dataset.openLine, 10);
        clickLine(ln);
        scrollToLine(ln);
      });
    });
  }

  // One comment in the panel: its text, the suggested change it carries, and the
  // reply and delete controls. Depth indents a reply under what it answers.
  function commentHtml(c, thread, lineNum) {
    const depth = replyDepth(c, thread);
    const suggest = c.suggestion
      ? `<pre class="comment-suggestion"><code>${escapeHtml(c.suggestion)}</code></pre>`
      : '';
    return `
      <div class="comment-body" style="margin-left:${depth * 0.9}rem" data-comment-id="${c.id}">
        <div class="comment-text">${escapeHtml(c.comment || '')}</div>
        ${suggest}
        <div class="comment-actions">
          <button class="link-btn" data-reply="${c.id}" data-reply-line="${lineNum}">Reply</button>
          <button class="link-btn danger" data-delete="${c.id}" data-delete-line="${lineNum}">Delete</button>
        </div>
      </div>
    `;
  }

  // How deep a comment sits below the comment the thread began with, which is
  // what the panel indents by. A reply to something unknown sits at the root.
  // thread is the array of comments the id is looked up in.
  function replyDepth(c, thread) {
    let depth = 0;
    for (let cur = c; cur && cur.replyTo && depth < 20; depth++) {
      const parent = thread.find(p => p.id === cur.replyTo);
      if (!parent) break;
      cur = parent;
    }
    return depth;
  }

  // Update editor visibility
  function updateEditorVisibility() {
    if (commentEditor) {
      commentEditor.style.display = state.editingLine ? '' : 'none';
    }
  }

  // Update status bar comment count
  function updateCommentCount() {
    if (!statusCommentCount) return;
    let count = 0;
    for (const file in state.allAnnotations) {
      for (const ann of Object.values(state.allAnnotations[file])) {
        count += (ann.comments || []).length;
      }
    }
    statusCommentCount.textContent = count + ' comment' + (count !== 1 ? 's' : '');
  }

  // Escape HTML. Replacing the few characters that matter is a fraction of the
  // cost of routing the text through a throwaway element, and this runs for
  // every tree row, every comment and every line of a diff tooltip.
  const htmlEscapes = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  function escapeHtml(str) {
    return String(str).replace(/[&<>"']/g, ch => htmlEscapes[ch]);
  }

  // Diff tooltip: the hunk under the pointer, shown with the base revision's
  // lines on the left and the reviewed tree's lines on the right. The left
  // side is there to be read — annotations stay where they belong, on the
  // lines of the tree under review.
  let diffTooltip = null;
  let diffTooltipHunk = null;
  let diffTooltipMoveHandler = null;
  let diffTooltipBounds = null;
  let diffTooltipRaf = null;
  let diffTooltipPinned = false;

  // sbLeftLabel names the revision the left column holds, abbreviated the way
  // git abbreviates it: the base the review is measured against, or, when a
  // single commit is being stepped through, that commit's parent, which is what
  // its diff is taken from.
  function sbLeftLabel() {
    if (!state.rev) return state.baseShort || shortRev(state.baseRev) || 'HEAD';
    const c = state.commits.find(c => c.hash === state.rev);
    if (!c) return shortRev(state.rev) + '^';
    return c.short + '^';
  }

  // shortRev abbreviates a revision the way git does. A named revision that is
  // not a hash — a branch or tag — is left alone: it has no longer form to
  // shorten, and the name is the useful thing to read.
  function shortRev(rev) {
    if (!rev) return '';
    return /^[0-9a-f]{40}$/i.test(rev) ? rev.slice(0, 7) : rev;
  }

  // hunkOrigin names the commit that put the hunk there, as a line for the
  // pane's header: the abbreviated hash, and the subject of its commit message
  // on hover for the reader who wants to know what it was. A hunk with no
  // commit to name — one only in the working tree — says so rather than leaving
  // the reader wondering what is missing.
  function hunkOrigin(hunk) {
    const c = hunk && hunk.commit;
    if (!c || !c.short) {
      return '<span class="sb-origin sb-origin-none" title="Not committed yet">uncommitted</span>';
    }
    const detail = [c.short, c.subject, c.author, c.date].filter(Boolean).join('\n');
    return '<span class="sb-origin" title="' + escapeHtml(detail) + '">' +
      escapeHtml(c.short) + '</span>';
  }
  // diffTooltipContent renders a hunk the way the current setting asks for.
  function diffTooltipContent(hunk) {
    return state.sideBySide ? hunkSideBySide(hunk) : hunkUnified(hunk);
  }

  // hunkSideBySide builds one hunk as two columns of numbered lines. A row
  // git replaced reads as a removal on the left and an addition on the right,
  // a row only one side has leaves the other blank.
  function hunkSideBySide(hunk) {
    const rows = hunk.rows || [];
    const cells = rows.map(row => {
      const both = row.oldNo && row.newNo;
      const equal = both && row.old === row.new;
      const oldCls = row.oldNo && !equal ? ' sb-del' : '';
      const newCls = row.newNo && !equal ? ' sb-add' : '';
      const oldBody = row.oldNo ? escapeHtml(row.old) : '';
      const newBody = row.newNo ? escapeHtml(row.new) : '';
      return '<div class="sb-row">' +
        sbCell('old', row.oldNo, oldCls, oldBody) +
        sbCell('new', row.newNo, newCls, newBody) +
        '</div>';
    }).join('');
    return '<div class="sb-head">' +
        '<span class="sb-col">' + escapeHtml(sbLeftLabel()) + '</span>' +
        '<span class="sb-col">' + escapeHtml(annotateTargetLabel()) + '</span>' +
        '<button type="button" class="sb-pin" title="Keep this hunk open beside the code">pin</button>' +
      '</div>' +
      '<div class="sb-origin-row">' + hunkOrigin(hunk) + '</div>' +
      '<div class="sb-grid">' + cells + '</div>' +
      '<div class="sb-foot">pinned — press Esc or click unpin to close</div>';
  }

  // sbCell is one side of a side-by-side row: its line number, when that side
  // has the line, and its text. A side without the line stays blank, with
  // nothing between it and the text it was measured against.
  function sbCell(side, lineNo, cls, body) {
    const cellCls = 'sb-cell sb-' + side + cls;
    if (!lineNo) return '<div class="' + cellCls + ' sb-blank"></div>';
    return '<div class="' + cellCls + '">' +
      '<span class="sb-no">' + lineNo + '</span>' +
      '<span class="sb-text">' + body + '</span>' +
      '</div>';
  }

  // annotateTargetLabel names the revision the reviewed side belongs to: the
  // commit being stepped through, or the working tree.
  function annotateTargetLabel() {
    if (!state.rev) return 'working tree';
    const c = state.commits.find(c => c.hash === state.rev);
    return c ? c.short : state.rev.slice(0, 8);
  }

  // hunkUnified is the older one-column rendering, used when the side-by-side
  // view is switched off. The hunk still arrives as rows, so the raw diff line
  // an older server would have sent is rebuilt from them.
  function hunkUnified(hunk) {
    const lines = [];
    for (const row of hunk.rows || []) {
      if (row.oldNo && row.newNo && row.old === row.new) {
        lines.push(' ' + row.old);
        continue;
      }
      if (row.oldNo) lines.push('-' + row.old);
      if (row.newNo) lines.push('+' + row.new);
    }
    return '<div class="sb-origin-row">' + hunkOrigin(hunk) + '</div>' +
      '<pre>' + lines.map(line => {
        const cls = line.startsWith('+') ? ' class="diff-add"' : line.startsWith('-') ? ' class="diff-del"' : '';
        return '<span' + cls + '>' + escapeHtml(line) + '</span>';
      }).join('\n') + '</pre>';
  }

  function showDiffTooltip(event, hunk) {
    // Moving along the gutter of one hunk keeps the tooltip that is up, rather
    // than building the same one again for every cell passed over.
    if (diffTooltip && diffTooltipHunk === hunk) return;

    // A pinned tooltip stays docked; a new hunk read into it replaces its
    // contents rather than moving it out from where the reader put it. The
    // header is rebuilt with it, so the button has to be told it is still the
    // pinned one.
    if (diffTooltipPinned && diffTooltip) {
      diffTooltipHunk = hunk;
      diffTooltip.innerHTML = diffTooltipContent(hunk);
      updatePinButton();
      return;
    }

    hideDiffTooltip();
    diffTooltipHunk = hunk;
    diffTooltip = document.createElement('div');
    diffTooltip.className = 'diff-tooltip' + (state.sideBySide ? ' side-by-side' : '');
    diffTooltip.innerHTML = diffTooltipContent(hunk);

    // Place the tooltip at the cursor straight away so the pointer is already
    // inside it (near the top-left corner). This lets a tall, scrollable
    // tooltip be reached and scrolled without the pointer having to leave it
    // first.
    const inset = 12;
    diffTooltip.style.left = Math.max(0, event.clientX - inset) + 'px';
    diffTooltip.style.top = Math.max(0, event.clientY - inset) + 'px';
    document.body.appendChild(diffTooltip);

    // Clamping the tooltip into the viewport needs its measured size, and
    // measuring forces the browser to lay out the page. On a large diff the
    // code view holds so many nodes that this layout costs hundreds of
    // milliseconds, and doing it here — while the pointer is crossing gutters
    // during a scroll — stalls the scroll until it finishes. So the measuring
    // and clamping are put off to the next frame, off the hover path, where the
    // layout has already settled and reading it back is cheap.
    const cursorX = event.clientX;
    const cursorY = event.clientY;
    diffTooltipRaf = requestAnimationFrame(() => {
      diffTooltipRaf = null;
      if (!diffTooltip) return;

      const margin = 16;
      const rect = diffTooltip.getBoundingClientRect();
      let left = cursorX - inset;
      let top = cursorY - inset;

      // Keep it inside the viewport by shifting (never flipping away from the
      // cursor, which would move it out from under the pointer).
      if (left + rect.width > window.innerWidth - margin) {
        left = window.innerWidth - margin - rect.width;
      }
      left = Math.max(margin, Math.min(left, cursorX));
      if (top + rect.height > window.innerHeight - margin) {
        top = window.innerHeight - margin - rect.height;
      }
      top = Math.max(margin, Math.min(top, cursorY));

      diffTooltip.style.left = left + 'px';
      diffTooltip.style.top = top + 'px';

      // The tooltip stays where it was put, so its bounds are measured once
      // here rather than on every movement of the pointer, which would force
      // the browser to work out the page's layout again each time.
      diffTooltipBounds = diffTooltip.getBoundingClientRect();
    });

    diffTooltip.addEventListener('click', (e) => {
      if (!e.target.closest('.sb-pin')) {
        // A click anywhere else on a pinned pane is left alone: it is there to
        // be read and copied from, and closing on any click would make
        // selecting text impossible. Esc and the button put it away.
        return;
      }
      if (diffTooltipPinned) unpinDiffTooltip();
      else pinDiffTooltip();
    });

    // Hide once the pointer leaves the tooltip. A document-level mousemove
    // guard is used instead of the gutter's mouseleave: the tooltip now sits
    // under the cursor (covering the gutter), so relying on the gutter would
    // hide it immediately and loop. Until the bounds are measured next frame
    // the pointer is still at the corner where the tooltip was placed, so there
    // is nothing to leave yet.
    diffTooltipMoveHandler = (e) => {
      if (diffTooltipPinned) return;
      const b = diffTooltipBounds;
      if (!b) return;
      if (e.clientX < b.left || e.clientX > b.right ||
          e.clientY < b.top || e.clientY > b.bottom) {
        hideDiffTooltip();
      }
    };
    document.addEventListener('mousemove', diffTooltipMoveHandler);
  }

  // pinDiffTooltip keeps the hunk on screen after the pointer leaves, as a
  // pane down the side of the code view where a tall hunk can be read and
  // scrolled at leisure.
  function pinDiffTooltip() {
    if (!diffTooltip || diffTooltipPinned) return;
    diffTooltipPinned = true;
    diffTooltip.classList.add('pinned');
    updatePinButton();

    // Docked to the right of the code view, below its header, so the hunk
    // stays beside the lines it belongs to. The stylesheet's minimum width is
    // honoured, so the docked pane is never narrower than a hovering one.
    const rect = codeContent.getBoundingClientRect();
    const margin = 12;
    const minWidth = parseFloat(getComputedStyle(diffTooltip).minWidth) || 0;
    const width = Math.min(Math.max(rect.width * 0.5, minWidth), 760);
    diffTooltip.style.left = Math.max(margin, rect.right - width - margin) + 'px';
    diffTooltip.style.top = (rect.top + margin) + 'px';
    diffTooltip.style.width = width + 'px';
    diffTooltip.style.maxHeight = Math.max(120, rect.height - 2 * margin) + 'px';
    diffTooltipBounds = null; // the move guard no longer applies
  }

  // unpinDiffTooltip puts the pane away. There is no pointer over it to keep a
  // hovering hunk alive, so it goes with the pinned state.
  function unpinDiffTooltip() {
    hideDiffTooltip();
  }

  function updatePinButton() {
    const pin = diffTooltip && diffTooltip.querySelector('.sb-pin');
    if (!pin) return;
    pin.textContent = diffTooltipPinned ? 'unpin' : 'pin';
    pin.classList.toggle('active', diffTooltipPinned);
  }

  function hideDiffTooltip() {
    if (diffTooltipRaf) {
      cancelAnimationFrame(diffTooltipRaf);
      diffTooltipRaf = null;
    }
    if (diffTooltipMoveHandler) {
      document.removeEventListener('mousemove', diffTooltipMoveHandler);
      diffTooltipMoveHandler = null;
    }
    if (diffTooltip) {
      diffTooltip.remove();
      diffTooltip = null;
    }
    diffTooltipHunk = null;
    diffTooltipBounds = null;
    diffTooltipPinned = false;
  }

  // Escape puts an unpinned tooltip away as well, so the keyboard alone is
  // enough to get one off the screen.
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && diffTooltip) hideDiffTooltip();
  });

  // Start a new review, moving the current one into the review history when
  // it is set up and deleting it otherwise.
  async function newReview() {
    // History may have been set up outside this page, and the destination is
    // dated on retirement, so the settings are read fresh for the prompt.
    try {
      applyConfig(await api('GET', '/api/config'));
    } catch (e) {
      console.error('Failed to reload config:', e);
    }

    let question;
    if (state.historyEnabled && state.historyDest) {
      question = 'Start a new review? The current REVIEW.md will be moved to ' +
        state.historyDest + '. Confirm?';
    } else if (state.historyEnabled) {
      question = 'Start a new review? Confirm?';
    } else {
      question = 'Start a new review? This will delete all existing comments.';
    }
    if (!confirm(question)) return;

    try {
      const result = await api('DELETE', '/api/review');
      state.allAnnotations = {};
      state.annotations = {};
      clearCommentMarkers();
      annotationsChanged();
      if (result.archived) {
        showToast('New review started — the old one was moved to ' + result.archived);
      } else {
        showToast('New review started');
      }
      // Refresh the destination named for the next review.
      loadConfig();
    } catch (e) {
      showToast('Failed to start new review: ' + e.message, true);
    }
  }

  // Expose functions for inline handlers
  window.app = {
    saveComment, deleteComment, closeEditor, newReview,
    showSuggestion, hideSuggestion, toggleSuggestion, cancelReply,
  };

})();
