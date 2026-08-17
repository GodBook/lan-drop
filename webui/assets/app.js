// LAN Drop Frontend Core Engine
(function () {
  const CHUNK_SIZE = 4 * 1024 * 1024; // 4MB per chunk
  const PARALLEL_CHUNKS = 3;          // concurrent chunk uploads per file
  const CHUNK_RETRY_LIMIT = 3;
  const RETRY_BASE_DELAY = 600;
  const WS_RETRY_MAX_DELAY = 30000;
  const FILE_PAGE_SIZE = 20;

  // State
  let ws = null;
  let wsConnected = false;
  let textFeed = [];
  let fileList = [];
  let deviceName = getOrCreateDeviceName();
  let listenersBound = false; // guard: register DOM listeners exactly once
  let wsReconnectTimer = null;
  let wsRetryAttempt = 0;
  let wsGeneration = 0;
  let filePage = 1;
  let fileTotal = 0;
  let fileQuery = "";
  let fileType = "all";
  let fileLoadSequence = 0;
  let searchDebounceTimer = null;
  const selectedFiles = new Set();
  const uploadTasks = new Map();

  // DOM Elements
  const serverNameEl = document.getElementById("server-name");
  const connStatusEl = document.getElementById("conn-status");
  const connTextEl = document.getElementById("conn-text");
  const textInputEl = document.getElementById("text-input");
  const btnSendText = document.getElementById("btn-send-text");
  const btnPasteSend = document.getElementById("btn-paste-send");
  const btnClearText = document.getElementById("btn-clear-text");
  const textFeedListEl = document.getElementById("text-feed-list");
  const textCountEl = document.getElementById("text-count");
  const dropzoneEl = document.getElementById("dropzone");
  const fileInputEl = document.getElementById("file-input");
  const uploadQueueEl = document.getElementById("upload-queue");
  const fileListEl = document.getElementById("file-list");
  const fileCountEl = document.getElementById("file-count");
  const fileSearchEl = document.getElementById("file-search");
  const fileTypeFilterEl = document.getElementById("file-type-filter");
  const fileSelectAllEl = document.getElementById("file-select-all");
  const btnDeleteSelectedEl = document.getElementById("btn-delete-selected");
  const btnPrevPageEl = document.getElementById("btn-prev-page");
  const btnNextPageEl = document.getElementById("btn-next-page");
  const pageStatusEl = document.getElementById("page-status");
  const toastContainerEl = document.getElementById("toast-container");
  const pinModalEl = document.getElementById("pin-modal");
  const pinInputEl = document.getElementById("pin-input");
  const btnSubmitPin = document.getElementById("btn-submit-pin");
  const previewModalEl = document.getElementById("preview-modal");
  const previewBodyEl = document.getElementById("preview-body");
  const btnShowQREl = document.getElementById("btn-show-qr");
  const qrModalEl = document.getElementById("qr-modal");
  const qrImageEl = document.getElementById("qr-image");
  const qrLoadingEl = document.getElementById("qr-loading");
  const qrDetailsEl = document.getElementById("qr-details");
  const qrURLEl = document.getElementById("qr-url");
  const qrPINHintEl = document.getElementById("qr-pin-hint");
  const btnCloseQREl = document.getElementById("btn-close-qr");
  const btnCopyQRURLEl = document.getElementById("btn-copy-qr-url");
  const desktopActionsEl = document.getElementById("desktop-actions");
  const btnDesktopOpenFolderEl = document.getElementById("btn-desktop-open-folder");
  const btnDesktopCopyAddressEl = document.getElementById("btn-desktop-copy-address");
  const btnDesktopSettingsEl = document.getElementById("btn-desktop-settings");
  const desktopSettingsModalEl = document.getElementById("desktop-settings-modal");
  const btnCloseDesktopSettingsEl = document.getElementById("btn-close-desktop-settings");
  const desktopAdapterSelectEl = document.getElementById("desktop-adapter-select");
  const desktopUploadDirEl = document.getElementById("desktop-upload-dir");
  const desktopConnectURLEl = document.getElementById("desktop-connect-url");
  const btnDesktopChangeFolderEl = document.getElementById("btn-desktop-change-folder");
  let qrImageObjectURL = "";
  let qrPreviousFocus = null;
  let qrRequestID = 0;

  // Init: listeners bind once; auth + data loading can re-run after PIN entry
  init();

  async function init() {
    removeSensitiveURLParameters();
    if (!listenersBound) {
      setupEventListeners();
      listenersBound = true;
    }
    if (isLoopbackHost(window.location.hostname)) {
      btnShowQREl.hidden = false;
    }
    if (window.__LANDROP_DESKTOP__) {
      desktopActionsEl.hidden = false;
      refreshDesktopSettings();
    }
    await checkAuthAndLoadInfo();
    connectWebSocket();
    loadFiles();
  }

  // Device Name resolution
  function getOrCreateDeviceName() {
    let name = localStorage.getItem("landrop_device_name");
    if (!name) {
      const ua = navigator.userAgent;
      let platform = "Device";
      if (/iPhone|iPad/i.test(ua)) platform = "iOS Device";
      else if (/Android/i.test(ua)) platform = "Android Phone";
      else if (/Mac/i.test(ua)) platform = "Mac";
      else if (/Windows/i.test(ua)) platform = "Windows PC";
      else if (/Linux/i.test(ua)) platform = "Linux";

      name = `${platform}-${Math.floor(100 + Math.random() * 900)}`;
      localStorage.setItem("landrop_device_name", name);
    }
    return name;
  }

  // Auth & Info
  async function checkAuthAndLoadInfo() {
    try {
      // /api/info is auth-exempt; probe an auth-enforced endpoint to detect
      // whether a PIN is required so the modal can actually appear.
      const [infoRes, probeRes] = await Promise.all([
        fetch("/api/info"),
        fetch("/api/files?page=1&page_size=1"),
      ]);
      if (infoRes.ok) {
        const data = await infoRes.json();
        serverNameEl.textContent = `${data.hostname || "LAN-Server"} (${data.host_ip}:${data.port})`;
      }
      if (probeRes.status === 401) {
        showPinModal();
      }
    } catch (err) {
      serverNameEl.textContent = "LAN Server";
    }
  }

  function showPinModal() {
    pinModalEl.style.display = "flex";
    pinInputEl.focus();
  }

  async function submitPin() {
    const pin = pinInputEl.value.trim();
    if (!pin) return;
    try {
      const res = await fetch("/api/auth", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pin }),
      });
      if (res.ok) {
        pinModalEl.style.display = "none";
        showToast("身份验证成功", "success");
        await checkAuthAndLoadInfo();
        wsRetryAttempt = 0;
        connectWebSocket();
        loadFiles();
      } else if (res.status === 429) {
        showToast("尝试过于频繁，请 30 秒后再试", "error");
      } else {
        showToast("PIN 码错误，请重新输入", "error");
        pinInputEl.value = "";
      }
    } catch (e) {
      showToast("连接失败: " + e.message, "error");
    }
  }

  // WebSocket Connection
  function connectWebSocket() {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
    if (!navigator.onLine) {
      setConnectionState("offline");
      return;
    }

    const generation = ++wsGeneration;
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      try { ws.close(); } catch (e) { /* already closing */ }
    }

    setConnectionState("connecting");
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(`${protocol}//${window.location.host}/api/ws`);

    ws.onopen = () => {
      if (generation !== wsGeneration) return;
      wsConnected = true;
      wsRetryAttempt = 0;
      setConnectionState("connected");
    };

    ws.onmessage = (event) => {
      if (generation !== wsGeneration) return;
      try {
        handleWSMessage(JSON.parse(event.data));
      } catch (e) {
        console.error("WS Parse error", e);
      }
    };

    ws.onerror = () => {
      if (generation === wsGeneration) setConnectionState("connecting");
    };

    ws.onclose = () => {
      if (generation !== wsGeneration) return;
      wsConnected = false;
      ws = null;
      scheduleWebSocketReconnect();
    };
  }

  function scheduleWebSocketReconnect() {
    clearTimeout(wsReconnectTimer);
    if (!navigator.onLine) {
      setConnectionState("offline");
      return;
    }
    const base = Math.min(1000 * Math.pow(2, wsRetryAttempt), WS_RETRY_MAX_DELAY);
    const delay = Math.round(base * (1 + Math.random() * 0.3));
    wsRetryAttempt += 1;
    setConnectionState("connecting", Math.max(1, Math.ceil(delay / 1000)));
    wsReconnectTimer = setTimeout(connectWebSocket, delay);
  }

  function setConnectionState(state, retrySeconds) {
    const states = {
      connected: ["var(--success)", "rgba(16, 185, 129, 0.1)", "已连接"],
      connecting: ["var(--warning)", "rgba(245, 158, 11, 0.12)", retrySeconds ? `${retrySeconds} 秒后重连` : "连接中..."],
      offline: ["var(--danger)", "rgba(239, 68, 68, 0.1)", "网络已离线"],
    };
    const value = states[state] || states.connecting;
    connStatusEl.style.color = value[0];
    connStatusEl.style.background = value[1];
    connStatusEl.querySelector(".status-dot").style.background = value[0];
    connTextEl.textContent = value[2];
  }

  function handleWSMessage(msg) {
    if (msg.type === "init_feed") {
      textFeed = msg.data || [];
      renderTextFeed();
    } else if (msg.type === "new_text") {
      textFeed.unshift(msg.data);
      renderTextFeed();
      if (msg.data.sender !== deviceName) {
        showToast(`收到来自 ${msg.data.sender} 的新文本`, "info");
        notifyUser(`📨 来自 ${msg.data.sender} 的新文本`, truncate(msg.data.content, 60));
      }
    } else if (msg.type === "new_file") {
      loadFiles();
      showToast(`新文件到达: ${msg.data.name}`, "success");
      notifyUser("新文件到达", msg.data.name);
    } else if (msg.type === "file_deleted") {
      selectedFiles.delete(msg.filename);
      loadFiles();
    }
  }

  // Text & Clipboard Actions
  function sendText(content) {
    if (!content || !content.trim()) return;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(
        JSON.stringify({
          type: "send_text",
          content: content.trim(),
          sender: deviceName,
        })
      );
      textInputEl.value = "";
      showToast("文本已发送", "success");
    } else {
      // HTTP Fallback
      fetch("/api/text/send", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: content.trim(), sender: deviceName }),
      }).then(() => {
        textInputEl.value = "";
        showToast("文本已发送", "success");
      });
    }
  }

  function renderTextFeed() {
    textCountEl.textContent = `${textFeed.length} 条`;
    textFeedListEl.innerHTML = "";

    if (textFeed.length === 0) {
      textFeedListEl.innerHTML = `<div style="text-align:center; color:var(--text-muted); padding:20px; font-size:13px;">暂无历史文本，输入内容即刻同步</div>`;
      return;
    }

    textFeed.forEach((item) => {
      const el = document.createElement("div");
      el.className = "feed-item";

      const timeStr = new Date(item.timestamp || Date.now()).toLocaleTimeString();
      const formattedContent = escapeAndFormatUrls(item.content);

      el.innerHTML = `
        <div class="feed-meta">
          <span><strong>${escapeHTML(item.sender || "Device")}</strong></span>
          <span>${timeStr}</span>
        </div>
        <div class="feed-content">${formattedContent}</div>
        <div style="display: flex; justify-content: flex-end;">
          <button class="btn-secondary btn-sm btn-copy">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            复制
          </button>
        </div>
      `;

      el.querySelector(".btn-copy").addEventListener("click", () => {
        copyToClipboard(item.content);
      });

      textFeedListEl.appendChild(el);
    });
  }

  function copyToClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(() => {
        showToast("已成功复制到剪贴板", "success");
      });
    } else {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
      showToast("已成功复制到剪贴板", "success");
    }
  }

  // File Upload: parallel chunk engine with resume support
  function handleFiles(files) {
    files.forEach((file) => createUploadTask(file));
  }

  function fileSignature(file) {
    return `${file.name}_${file.size}_${file.lastModified}`;
  }

  async function createUploadTask(file) {
    const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE));
    const sig = fileSignature(file);

    if (Array.from(uploadTasks.values()).some((task) => task.sig === sig && !["cancelled", "done"].includes(task.state))) {
      showToast(`“${file.name}”已在传输队列中`, "info");
      return;
    }

    let fileID = localStorage.getItem("landrop_upload_" + sig);
    if (fileID) {
      const check = await fetch(`/api/upload/status?file_id=${encodeURIComponent(fileID)}`).catch(() => null);
      if (!check || !check.ok) fileID = null;
    }
    if (!fileID) {
      fileID = `${Date.now()}_${Math.random().toString(36).substr(2, 9)}`;
      localStorage.setItem("landrop_upload_" + sig, fileID);
    }

    const progressCard = document.createElement("div");
    progressCard.className = "progress-item";
    progressCard.innerHTML = `
      <div class="progress-info">
        <span class="file-name">${escapeHTML(file.name)}</span>
        <div class="upload-actions">
          <span class="progress-pct">0%</span>
          <button class="icon-button btn-upload-toggle" type="button" title="暂停" aria-label="暂停上传">
            <svg class="icon-pause" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M8 5v14M16 5v14"></path></svg>
            <svg class="icon-play" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true" hidden><path d="m7 4 13 8-13 8z"></path></svg>
          </button>
          <button class="icon-button btn-upload-cancel" type="button" title="取消" aria-label="取消上传">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"></path></svg>
          </button>
        </div>
      </div>
      <div class="progress-bar-bg">
        <div class="progress-bar-fill"></div>
      </div>
      <div class="progress-info" style="color: var(--text-muted); font-size: 11px;">
        <span class="speed">准备传输...</span>
        <span>${formatBytes(file.size)}</span>
      </div>
    `;
    uploadQueueEl.prepend(progressCard);

    const fillEl = progressCard.querySelector(".progress-bar-fill");
    const pctEl = progressCard.querySelector(".progress-pct");
    const speedEl = progressCard.querySelector(".speed");
    const task = {
      file,
      fileID,
      sig,
      totalChunks,
      state: "running",
      runToken: 0,
      uploadedBytes: 0,
      startedAt: Date.now(),
      completedChunks: new Set(),
      controllers: new Set(),
      card: progressCard,
      fillEl,
      pctEl,
      speedEl,
      toggleEl: progressCard.querySelector(".btn-upload-toggle"),
      cancelEl: progressCard.querySelector(".btn-upload-cancel"),
    };
    uploadTasks.set(fileID, task);
    task.toggleEl.addEventListener("click", () => {
      if (task.state === "running") pauseUpload(task);
      else if (task.state === "paused" || task.state === "failed") resumeUpload(task);
    });
    task.cancelEl.addEventListener("click", () => cancelUpload(task));
    updateUploadTaskUI(task);
    runUploadTask(task);
  }

  function updateUploadTaskUI(task) {
    const denominator = Math.max(task.file.size, 1);
    const percent = task.file.size === 0 && task.completedChunks.size > 0
      ? 100
      : Math.min(100, Math.round((task.uploadedBytes / denominator) * 100));
    task.fillEl.style.width = `${percent}%`;
    task.pctEl.textContent = `${percent}%`;
    const pauseIcon = task.toggleEl.querySelector(".icon-pause");
    const playIcon = task.toggleEl.querySelector(".icon-play");
    const canStart = task.state === "paused" || task.state === "failed";
    const finalizing = task.state === "finalizing";
    pauseIcon.hidden = canStart;
    playIcon.hidden = !canStart;
    task.toggleEl.disabled = finalizing || task.state === "done";
    task.cancelEl.disabled = finalizing || task.state === "done";
    task.toggleEl.title = finalizing ? "正在完成" : task.state === "failed" ? "重试" : canStart ? "继续" : "暂停";
    task.toggleEl.setAttribute("aria-label", `${task.toggleEl.title}上传`);
    task.card.dataset.state = task.state;
    if (task.state === "running") {
      const elapsedSec = Math.max((Date.now() - task.startedAt) / 1000, 0.1);
      task.speedEl.style.color = "";
      task.speedEl.textContent = task.uploadedBytes > 0 ? `${formatBytes(task.uploadedBytes / elapsedSec)}/s` : "准备传输...";
    }
  }

  function pauseUpload(task) {
    if (task.state !== "running") return;
    task.state = "paused";
    task.runToken += 1;
    abortTaskRequests(task);
    task.speedEl.textContent = "已暂停";
    updateUploadTaskUI(task);
  }

  function resumeUpload(task) {
    if (task.state !== "paused" && task.state !== "failed") return;
    const wasFailed = task.state === "failed";
    task.state = "running";
    task.startedAt = Date.now();
    task.speedEl.textContent = wasFailed ? "正在重试..." : "正在继续...";
    updateUploadTaskUI(task);
    runUploadTask(task);
  }

  async function cancelUpload(task) {
    if (task.state === "cancelled" || task.state === "done" || task.state === "finalizing") return;
    task.state = "cancelled";
    task.runToken += 1;
    abortTaskRequests(task);
    localStorage.removeItem("landrop_upload_" + task.sig);
    uploadTasks.delete(task.fileID);
    task.card.remove();
    try {
      await fetch("/api/upload/cancel", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ file_id: task.fileID }),
      });
    } catch (e) { /* stale chunks are also removed by the server sweeper */ }
    showToast(`已取消“${task.file.name}”`, "info");
  }

  function abortTaskRequests(task) {
    task.controllers.forEach((controller) => controller.abort());
    task.controllers.clear();
  }

  async function runUploadTask(task) {
    const token = ++task.runToken;
    try {
      const statusRes = await fetch(`/api/upload/status?file_id=${encodeURIComponent(task.fileID)}`);
      if (!statusRes.ok) throw new Error("无法读取续传状态");
      const status = await statusRes.json();
      if (token !== task.runToken || task.state !== "running") return;

      task.completedChunks = new Set((status.chunks || []).filter((i) => Number.isInteger(i) && i >= 0 && i < task.totalChunks));
      task.uploadedBytes = 0;
      task.completedChunks.forEach((i) => {
        task.uploadedBytes += Math.max(0, Math.min(CHUNK_SIZE, task.file.size - i * CHUNK_SIZE));
      });
      updateUploadTaskUI(task);

      const pending = [];
      for (let i = 0; i < task.totalChunks; i++) {
        if (!task.completedChunks.has(i)) pending.push(i);
      }

      let next = 0;
      const worker = async () => {
        while (next < pending.length && token === task.runToken && task.state === "running") {
          const index = pending[next++];
          await uploadChunkWithRetry(task, index, token);
        }
      };
      const workers = Array.from(
        { length: Math.min(PARALLEL_CHUNKS, pending.length) },
        () => worker()
      );
      await Promise.all(workers);
      if (token !== task.runToken || task.state !== "running") return;

      task.state = "finalizing";
      task.speedEl.textContent = "正在合并落地，请稍候...";
      updateUploadTaskUI(task);
      const completeRes = await fetch("/api/upload/complete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          file_id: task.fileID,
          filename: task.file.name,
          total_chunks: task.totalChunks,
          file_size: task.file.size,
        }),
      });
      if (!completeRes.ok) throw new Error(await responseError(completeRes, "文件合并失败"));
      if (token !== task.runToken || task.state !== "finalizing") return;

      task.state = "done";
      task.uploadedBytes = Math.max(task.file.size, 1);
      localStorage.removeItem("landrop_upload_" + task.sig);
      updateUploadTaskUI(task);
      task.speedEl.textContent = "上传完成";
      setTimeout(() => {
        uploadTasks.delete(task.fileID);
        task.card.remove();
      }, 2500);
      loadFiles();
    } catch (err) {
      if (token !== task.runToken || task.state === "paused" || task.state === "cancelled" || err.name === "AbortError") return;
      task.state = "failed";
      abortTaskRequests(task);
      task.speedEl.textContent = `上传中断: ${err.message}`;
      task.speedEl.style.color = "var(--danger)";
      updateUploadTaskUI(task);
    }
  }

  async function uploadChunkWithRetry(task, index, token) {
    const start = index * CHUNK_SIZE;
    const end = Math.min(start + CHUNK_SIZE, task.file.size);
    const chunkBlob = task.file.slice(start, end);

    for (let attempt = 0; attempt <= CHUNK_RETRY_LIMIT; attempt++) {
      if (token !== task.runToken || task.state !== "running") throw new DOMException("上传已停止", "AbortError");
      const controller = new AbortController();
      task.controllers.add(controller);
      try {
        const formData = new FormData();
        formData.append("file_id", task.fileID);
        formData.append("chunk_index", index.toString());
        formData.append("total_chunks", task.totalChunks.toString());
        formData.append("filename", task.file.name);
        formData.append("file_size", task.file.size.toString());
        formData.append("chunk", chunkBlob);
        const res = await fetch("/api/upload/chunk", { method: "POST", body: formData, signal: controller.signal });
        if (!res.ok) {
          const message = await responseError(res, `分片 ${index + 1} 上传失败`);
          const error = new Error(message);
          error.nonRetryable = [400, 409, 413, 507].includes(res.status);
          throw error;
        }
        if (!task.completedChunks.has(index)) {
          task.completedChunks.add(index);
          task.uploadedBytes += chunkBlob.size;
          updateUploadTaskUI(task);
        }
        return;
      } catch (err) {
        if (err.name === "AbortError" || err.nonRetryable || attempt === CHUNK_RETRY_LIMIT) throw err;
        const wait = RETRY_BASE_DELAY * Math.pow(2, attempt) * (1 + Math.random() * 0.25);
        task.speedEl.textContent = `分片重试 ${attempt + 1}/${CHUNK_RETRY_LIMIT}...`;
        await delay(wait);
      } finally {
        task.controllers.delete(controller);
      }
    };
  }

  // File List & Management
  function applyFileFilters() {
    // Read the controls together so a type change cannot issue a request with
    // a search term that is still waiting for its debounce timer.
    fileQuery = fileSearchEl.value.trim();
    fileType = fileTypeFilterEl.value;
    filePage = 1;
    loadFiles();
  }

  async function loadFiles() {
    const sequence = ++fileLoadSequence;
    try {
      const params = new URLSearchParams({
        page: String(filePage),
        page_size: String(FILE_PAGE_SIZE),
        type: fileType,
      });
      if (fileQuery) params.set("q", fileQuery);
      const res = await fetch(`/api/files?${params.toString()}`);
      if (!res.ok) return;
      const data = await res.json();
      if (sequence !== fileLoadSequence) return;
      fileList = data.files || [];
      fileTotal = Number.isFinite(Number(data.total)) ? Number(data.total) : fileList.length;
      filePage = Number.isFinite(Number(data.page)) ? Math.max(1, Number(data.page)) : filePage;
      const maxPage = Math.max(1, Math.ceil(fileTotal / FILE_PAGE_SIZE));
      if (filePage > maxPage) {
        filePage = maxPage;
        loadFiles();
        return;
      }
      selectedFiles.clear();
      renderFileList();
    } catch (e) {
      console.error("Load files error", e);
    }
  }

  function isPreviewable(name) {
    return /\.(png|jpe?g|gif|webp|bmp|svg|mp4|webm|mov|m4v|mp3|wav|ogg|m4a)$/i.test(name);
  }

  function renderFileList() {
    const totalPages = Math.max(1, Math.ceil(fileTotal / FILE_PAGE_SIZE));
    fileCountEl.textContent = `${fileTotal} 个`;
    pageStatusEl.textContent = `第 ${filePage} / ${totalPages} 页`;
    btnPrevPageEl.disabled = filePage <= 1;
    btnNextPageEl.disabled = filePage >= totalPages;
    fileListEl.innerHTML = "";

    if (fileList.length === 0) {
      fileListEl.innerHTML = `<div class="empty-state">${fileQuery || fileType !== "all" ? "没有符合条件的文件" : "暂无已传输文件"}</div>`;
      updateBatchControls();
      return;
    }

    fileList.forEach((file) => {
      const card = document.createElement("div");
      card.className = "file-card";

      const timeStr = new Date(file.mod_time).toLocaleString();
      const safeURL = escapeHTML(file.url || "");
      const previewable = isPreviewable(file.name);

      card.innerHTML = `
        <label class="file-select" title="选择 ${escapeHTML(file.name)}">
          <input type="checkbox" class="file-checkbox" aria-label="选择 ${escapeHTML(file.name)}">
        </label>
        <div class="file-info-group">
          <div class="file-icon">📄</div>
          <div class="file-details">
            <div class="file-name" title="${escapeHTML(file.name)}">${escapeHTML(file.name)}</div>
            <div class="file-meta">${formatBytes(file.size)} • ${timeStr}</div>
          </div>
        </div>
        <div class="file-actions">
          ${previewable ? `
          <button class="btn-secondary btn-sm btn-preview" title="预览">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle></svg>
            预览
          </button>` : ""}
          <a href="${safeURL}" download="${escapeHTML(file.name)}" class="btn-primary btn-sm" style="text-decoration:none;">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
            下载
          </a>
          <button class="btn-danger btn-sm btn-del">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
          </button>
        </div>
      `;

      const previewBtn = card.querySelector(".btn-preview");
      if (previewBtn) previewBtn.addEventListener("click", () => openPreview(file));
      card.querySelector(".btn-del").addEventListener("click", () => deleteFile(file.name));
      const checkbox = card.querySelector(".file-checkbox");
      checkbox.checked = selectedFiles.has(file.name);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) selectedFiles.add(file.name);
        else selectedFiles.delete(file.name);
        card.classList.toggle("selected", checkbox.checked);
        updateBatchControls();
      });
      card.classList.toggle("selected", checkbox.checked);
      fileListEl.appendChild(card);
    });
    updateBatchControls();
  }

  function updateBatchControls() {
    const allSelected = fileList.length > 0 && fileList.every((file) => selectedFiles.has(file.name));
    fileSelectAllEl.checked = allSelected;
    fileSelectAllEl.indeterminate = !allSelected && selectedFiles.size > 0;
    btnDeleteSelectedEl.disabled = selectedFiles.size === 0;
    btnDeleteSelectedEl.lastChild.textContent = selectedFiles.size > 0 ? ` 删除所选 (${selectedFiles.size})` : " 删除所选";
  }

  // Media preview overlay (images / video / audio served inline)
  function openPreview(file) {
    const url = `${file.url}?inline=1`;
    const name = escapeHTML(file.name);
    let media = "";
    if (/\.(png|jpe?g|gif|webp|bmp|svg)$/i.test(file.name)) {
      media = `<img src="${escapeHTML(url)}" alt="${name}">`;
    } else if (/\.(mp4|webm|mov|m4v)$/i.test(file.name)) {
      media = `<video src="${escapeHTML(url)}" controls autoplay></video>`;
    } else {
      media = `<audio src="${escapeHTML(url)}" controls autoplay></audio>`;
    }
    previewBodyEl.innerHTML = `<div class="preview-title">${name}</div>${media}`;
    previewModalEl.style.display = "flex";
  }

  function closePreview() {
    previewModalEl.style.display = "none";
    previewBodyEl.innerHTML = ""; // stop media playback
  }

  async function openQRModal() {
    const requestID = ++qrRequestID;
    qrPreviousFocus = document.activeElement;
    btnShowQREl.setAttribute("aria-expanded", "true");
    qrModalEl.style.display = "flex";
    qrImageEl.hidden = true;
    qrDetailsEl.hidden = true;
    qrLoadingEl.hidden = false;
    qrLoadingEl.textContent = "正在生成二维码...";
    btnCloseQREl.focus();

    try {
      const [detailsRes, imageRes] = await Promise.all([
        fetch("/api/qr?format=json", { cache: "no-store" }),
        fetch("/api/qr", { cache: "no-store" }),
      ]);
      if (!detailsRes.ok || !imageRes.ok) {
        throw new Error(detailsRes.status === 401 || imageRes.status === 401 ? "请先完成身份验证" : "二维码生成失败");
      }

      const details = await detailsRes.json();
      const imageBlob = await imageRes.blob();
      if (requestID !== qrRequestID || qrModalEl.style.display === "none") return;
      if (qrImageObjectURL) URL.revokeObjectURL(qrImageObjectURL);
      qrImageObjectURL = URL.createObjectURL(imageBlob);
      qrImageEl.src = qrImageObjectURL;
      qrURLEl.textContent = details.url || "";
      qrPINHintEl.textContent = details.pin ? `访问 PIN：${details.pin}` : "无需访问 PIN";
      qrLoadingEl.hidden = true;
      qrImageEl.hidden = false;
      qrDetailsEl.hidden = false;
    } catch (err) {
      if (requestID !== qrRequestID || qrModalEl.style.display === "none") return;
      qrLoadingEl.textContent = err.message || "二维码加载失败";
      showToast(qrLoadingEl.textContent, "error");
    }
  }

  function closeQRModal() {
    qrRequestID++;
    qrModalEl.style.display = "none";
    btnShowQREl.setAttribute("aria-expanded", "false");
    if (qrImageObjectURL) {
      URL.revokeObjectURL(qrImageObjectURL);
      qrImageObjectURL = "";
      qrImageEl.removeAttribute("src");
    }
    if (qrPreviousFocus && typeof qrPreviousFocus.focus === "function") {
      qrPreviousFocus.focus();
    }
  }

  async function refreshDesktopSettings() {
    if (typeof window.desktopGetSettings !== "function") return;
    try {
      const info = await window.desktopGetSettings();
      desktopAdapterSelectEl.innerHTML = "";
      (info.adapters || []).forEach((adapter) => {
        const option = document.createElement("option");
        option.value = adapter.ip;
        option.textContent = `${adapter.name || "局域网网卡"} (${adapter.ip})`;
        option.selected = adapter.ip === info.selected_ip;
        desktopAdapterSelectEl.appendChild(option);
      });
      desktopUploadDirEl.textContent = info.upload_dir || "";
      desktopConnectURLEl.textContent = info.connect_url || "";
    } catch (err) {
      console.error("Desktop bridge error", err);
    }
  }

  async function openDesktopSettings() {
    if (!window.__LANDROP_DESKTOP__) return;
    await refreshDesktopSettings();
    desktopSettingsModalEl.style.display = "flex";
    desktopAdapterSelectEl.focus();
  }

  function closeDesktopSettings() {
    desktopSettingsModalEl.style.display = "none";
  }

  async function selectDesktopAdapter() {
    if (typeof window.desktopSetAdapter !== "function") return;
    desktopAdapterSelectEl.disabled = true;
    try {
      await window.desktopSetAdapter(desktopAdapterSelectEl.value);
      await Promise.all([refreshDesktopSettings(), checkAuthAndLoadInfo()]);
      showToast("连接网卡已切换", "success");
    } catch (err) {
      showToast("切换网卡失败: " + err.message, "error");
    } finally {
      desktopAdapterSelectEl.disabled = false;
    }
  }

  async function chooseDesktopUploadDirectory() {
    if (typeof window.desktopChooseUploadDir !== "function") return;
    btnDesktopChangeFolderEl.disabled = true;
    try {
      await window.desktopChooseUploadDir();
      await Promise.all([refreshDesktopSettings(), checkAuthAndLoadInfo(), loadFiles()]);
    } catch (err) {
      showToast("更改目录失败: " + err.message, "error");
    } finally {
      btnDesktopChangeFolderEl.disabled = false;
    }
  }

  async function callDesktopAction(name, successText) {
    const action = window[name];
    if (typeof action !== "function") return;
    try {
      await action();
      if (successText) showToast(successText, "success");
    } catch (err) {
      showToast(err.message || "桌面操作失败", "error");
    }
  }

  async function deleteFile(fileName) {
    if (!confirm(`确定在服务端删除文件 "${fileName}" 吗？`)) return;
    await deleteFiles([fileName]);
  }

  async function deleteSelectedFiles() {
    const names = Array.from(selectedFiles);
    if (names.length === 0) return;
    if (!confirm(`确定在服务端删除所选的 ${names.length} 个文件吗？此操作无法撤销。`)) return;
    await deleteFiles(names);
  }

  async function deleteFiles(names) {
    try {
      const res = await fetch("/api/files/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(names.length === 1 ? { filename: names[0] } : { filenames: names }),
      });
      if (res.ok) {
        names.forEach((name) => selectedFiles.delete(name));
        showToast(names.length === 1 ? "文件已删除" : `已删除 ${names.length} 个文件`, "info");
        await loadFiles();
      } else {
        throw new Error(await responseError(res, "删除请求被拒绝"));
      }
    } catch (e) {
      showToast("删除失败: " + e.message, "error");
    }
  }

  // Desktop notifications (only fire when the tab is in the background)
  function notifyUser(title, body) {
    if (document.hidden && "Notification" in window && Notification.permission === "granted") {
      try {
        new Notification(title, { body, icon: "/favicon.svg" });
      } catch (e) { /* some browsers restrict notification constructors */ }
    }
  }

  function requestNotifyPermission() {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission();
    }
  }

  // Toast UI
  function showToast(text, type = "info") {
    const toast = document.createElement("div");
    toast.className = "toast";
    if (type === "success") toast.style.borderLeftColor = "var(--success)";
    if (type === "error") toast.style.borderLeftColor = "var(--danger)";
    toast.textContent = text;
    toastContainerEl.appendChild(toast);
    setTimeout(() => toast.remove(), 3500);
  }

  // Event Listeners (bound exactly once)
  function setupEventListeners() {
    btnSendText.addEventListener("click", () => sendText(textInputEl.value));
    btnClearText.addEventListener("click", () => (textInputEl.value = ""));

    btnPasteSend.addEventListener("click", async () => {
      try {
        if (navigator.clipboard) {
          const clipText = await navigator.clipboard.readText();
          if (clipText) {
            sendText(clipText);
            return;
          }
        }
      } catch (e) {
        // clipboard read may need focus / permission
      }
      if (textInputEl.value) {
        sendText(textInputEl.value);
      } else {
        showToast("请手动在输入框粘贴后点击发送", "info");
      }
    });

    btnSubmitPin.addEventListener("click", submitPin);
    pinInputEl.addEventListener("keypress", (e) => {
      if (e.key === "Enter") submitPin();
    });

    // Dropzone
    dropzoneEl.addEventListener("click", () => fileInputEl.click());
    fileInputEl.addEventListener("change", (e) => {
      if (e.target.files && e.target.files.length > 0) {
        handleFiles(Array.from(e.target.files));
        fileInputEl.value = "";
      }
    });

    fileSearchEl.addEventListener("input", () => {
      clearTimeout(searchDebounceTimer);
      searchDebounceTimer = setTimeout(() => {
        searchDebounceTimer = null;
        applyFileFilters();
      }, 250);
    });
    fileTypeFilterEl.addEventListener("change", () => {
      clearTimeout(searchDebounceTimer);
      searchDebounceTimer = null;
      applyFileFilters();
    });
    fileSelectAllEl.addEventListener("change", () => {
      fileList.forEach((file) => {
        if (fileSelectAllEl.checked) selectedFiles.add(file.name);
        else selectedFiles.delete(file.name);
      });
      renderFileList();
    });
    btnDeleteSelectedEl.addEventListener("click", deleteSelectedFiles);
    btnPrevPageEl.addEventListener("click", () => {
      if (filePage <= 1) return;
      filePage -= 1;
      loadFiles();
    });
    btnNextPageEl.addEventListener("click", () => {
      if (filePage >= Math.max(1, Math.ceil(fileTotal / FILE_PAGE_SIZE))) return;
      filePage += 1;
      loadFiles();
    });

    ["dragenter", "dragover"].forEach((eventName) => {
      document.body.addEventListener(eventName, (e) => {
        e.preventDefault();
        dropzoneEl.classList.add("dragover");
      });
    });

    ["dragleave", "drop"].forEach((eventName) => {
      document.body.addEventListener(eventName, (e) => {
        e.preventDefault();
        dropzoneEl.classList.remove("dragover");
      });
    });

    document.body.addEventListener("drop", (e) => {
      e.preventDefault();
      if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
        handleFiles(Array.from(e.dataTransfer.files));
      }
    });

    // Clipboard screenshot paste: image goes straight to file transfer
    document.addEventListener("paste", (e) => {
      const items = e.clipboardData && e.clipboardData.items;
      if (!items) return;
      for (const item of items) {
        if (item.type && item.type.startsWith("image/")) {
          const blob = item.getAsFile();
          if (!blob) continue;
          const ext = (item.type.split("/")[1] || "png").replace("jpeg", "jpg");
          const ts = new Date().toISOString().replace(/[-:T]/g, "").slice(0, 14);
          const file = new File([blob], `剪贴板图片_${ts}.${ext}`, { type: item.type });
          showToast("检测到剪贴板图片，开始传输...", "info");
          handleFiles([file]);
          return;
        }
      }
    });

    // Preview modal close
    if (previewModalEl) {
      previewModalEl.addEventListener("click", closePreview);
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") closePreview();
      });
    }

    btnShowQREl.addEventListener("click", openQRModal);
    btnCloseQREl.addEventListener("click", closeQRModal);
    btnCopyQRURLEl.addEventListener("click", () => copyToClipboard(qrURLEl.textContent));
    qrModalEl.addEventListener("click", (e) => {
      if (e.target === qrModalEl) closeQRModal();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && qrModalEl.style.display !== "none") closeQRModal();
    });

    btnDesktopOpenFolderEl.addEventListener("click", () => callDesktopAction("desktopOpenUploadDir"));
    btnDesktopCopyAddressEl.addEventListener("click", () => callDesktopAction("desktopCopyConnectionAddress", "连接地址已复制"));
    btnDesktopSettingsEl.addEventListener("click", openDesktopSettings);
    btnCloseDesktopSettingsEl.addEventListener("click", closeDesktopSettings);
    btnDesktopChangeFolderEl.addEventListener("click", chooseDesktopUploadDirectory);
    desktopAdapterSelectEl.addEventListener("change", selectDesktopAdapter);
    desktopSettingsModalEl.addEventListener("click", (event) => {
      if (event.target === desktopSettingsModalEl) closeDesktopSettings();
    });
    window.addEventListener("landrop:open-desktop-settings", openDesktopSettings);
    window.addEventListener("landrop:desktop-settings-changed", async () => {
      await Promise.all([refreshDesktopSettings(), checkAuthAndLoadInfo(), loadFiles()]);
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && desktopSettingsModalEl.style.display !== "none") closeDesktopSettings();
    });

    window.addEventListener("online", () => {
      wsRetryAttempt = 0;
      connectWebSocket();
      loadFiles();
    });
    window.addEventListener("offline", () => {
      clearTimeout(wsReconnectTimer);
      wsGeneration += 1;
      wsConnected = false;
      if (ws) {
        ws.onclose = null;
        try { ws.close(); } catch (e) { /* already closed */ }
        ws = null;
      }
      setConnectionState("offline");
    });

    // Ask for notification permission after the first interaction
    document.addEventListener("click", requestNotifyPermission, { once: true });
  }

  // Utilities
  function removeSensitiveURLParameters() {
    const current = new URL(window.location.href);
    if (!current.searchParams.has("pin")) return;
    current.searchParams.delete("pin");
    const clean = current.pathname + (current.searchParams.toString() ? `?${current.searchParams.toString()}` : "") + current.hash;
    window.history.replaceState(null, document.title, clean);
  }

  function delay(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  async function responseError(response, fallback) {
    try {
      const text = (await response.text()).trim();
      return text || `${fallback} (${response.status})`;
    } catch (e) {
      return `${fallback} (${response.status})`;
    }
  }

  function formatBytes(bytes) {
    bytes = Number(bytes) || 0;
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
  }

  function isLoopbackHost(hostname) {
    return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
  }

  function truncate(str, n) {
    if (!str) return "";
    return str.length > n ? str.slice(0, n) + "…" : str;
  }

  function escapeHTML(str) {
    if (!str) return "";
    return String(str).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function escapeAndFormatUrls(str) {
    if (!str) return "";
    const escaped = escapeHTML(str);
    const urlRegex = /(https?:\/\/[^\s]+)/g;
    return escaped.replace(urlRegex, (url) => `<a href="${url}" target="_blank" rel="noopener noreferrer">${url}</a>`);
  }
})();
