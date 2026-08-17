// LAN Drop Frontend Core Engine
(function () {
  const CHUNK_SIZE = 4 * 1024 * 1024; // 4MB per chunk
  const PARALLEL_CHUNKS = 3;          // concurrent chunk uploads per file

  // State
  let ws = null;
  let wsConnected = false;
  let textFeed = [];
  let fileList = [];
  let deviceName = getOrCreateDeviceName();
  let listenersBound = false; // guard: register DOM listeners exactly once

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
  let qrImageObjectURL = "";
  let qrPreviousFocus = null;
  let qrRequestID = 0;

  // Init: listeners bind once; auth + data loading can re-run after PIN entry
  init();

  async function init() {
    if (!listenersBound) {
      setupEventListeners();
      listenersBound = true;
    }
    if (isLoopbackHost(window.location.hostname)) {
      btnShowQREl.hidden = false;
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
        fetch("/api/files"),
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
    // Replace any existing socket: its stale onclose must not schedule retries
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      try { ws.close(); } catch (e) { /* already closing */ }
    }

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${protocol}//${window.location.host}/api/ws`;

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
      wsConnected = true;
      connStatusEl.style.color = "var(--success)";
      connStatusEl.style.background = "rgba(16, 185, 129, 0.1)";
      connTextEl.textContent = "已连接";
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        handleWSMessage(msg);
      } catch (e) {
        console.error("WS Parse error", e);
      }
    };

    ws.onclose = () => {
      wsConnected = false;
      connStatusEl.style.color = "var(--danger)";
      connStatusEl.style.background = "rgba(239, 68, 68, 0.1)";
      connTextEl.textContent = "重连中...";
      setTimeout(connectWebSocket, 2000);
    };
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
      // Skip if the file is already rendered (the uploader's own refresh)
      if (!fileList.some((f) => f.name === msg.data.name)) {
        fileList.unshift(msg.data);
        renderFileList();
        showToast(`新文件到达: ${msg.data.name}`, "success");
        notifyUser(`📁 新文件到达`, msg.data.name);
      }
    } else if (msg.type === "file_deleted") {
      fileList = fileList.filter((f) => f.name !== msg.filename);
      renderFileList();
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
  async function handleFiles(files) {
    for (const file of files) {
      await uploadFileInChunks(file);
    }
  }

  function fileSignature(file) {
    return `${file.name}_${file.size}_${file.lastModified}`;
  }

  async function uploadFileInChunks(file) {
    const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE));
    const sig = fileSignature(file);

    // Resume: reuse the previous file_id for the same file so already-uploaded
    // chunks can be skipped (survives page refresh via localStorage)
    let fileID = localStorage.getItem("landrop_upload_" + sig);
    if (fileID) {
      const check = await fetch(`/api/upload/status?file_id=${encodeURIComponent(fileID)}`).catch(() => null);
      if (!check || !check.ok) fileID = null;
    }
    if (!fileID) {
      fileID = `${Date.now()}_${Math.random().toString(36).substr(2, 9)}`;
      localStorage.setItem("landrop_upload_" + sig, fileID);
    }

    // Create progress bar card
    const progressCard = document.createElement("div");
    progressCard.className = "progress-item";
    progressCard.innerHTML = `
      <div class="progress-info">
        <span class="file-name">${escapeHTML(file.name)}</span>
        <span class="progress-pct">0%</span>
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

    const startTime = Date.now();
    let uploadedBytes = 0;
    let failed = false;

    const updateProgress = () => {
      const percent = Math.round((uploadedBytes / file.size) * 100);
      fillEl.style.width = `${percent}%`;
      pctEl.textContent = `${percent}%`;
      const elapsedSec = (Date.now() - startTime) / 1000;
      if (elapsedSec > 0) {
        speedEl.textContent = `${formatBytes(uploadedBytes / elapsedSec)}/s`;
      }
    };

    try {
      // Ask the server which chunks already exist (resume path)
      let existing = new Set();
      const statusRes = await fetch(`/api/upload/status?file_id=${encodeURIComponent(fileID)}`);
      if (statusRes.ok) {
        const data = await statusRes.json();
        (data.chunks || []).forEach((i) => existing.add(i));
        existing.forEach((i) => {
          if (i < totalChunks) uploadedBytes += Math.min(CHUNK_SIZE, file.size - i * CHUNK_SIZE);
        });
        updateProgress();
      }

      const pending = [];
      for (let i = 0; i < totalChunks; i++) {
        if (!existing.has(i)) pending.push(i);
      }

      const uploadChunk = async (i) => {
        const start = i * CHUNK_SIZE;
        const end = Math.min(start + CHUNK_SIZE, file.size);
        const chunkBlob = file.slice(start, end);

        const formData = new FormData();
        formData.append("file_id", fileID);
        formData.append("chunk_index", i.toString());
        formData.append("total_chunks", totalChunks.toString());
        formData.append("filename", file.name);
        formData.append("file_size", file.size.toString());
        formData.append("chunk", chunkBlob);

        const res = await fetch("/api/upload/chunk", {
          method: "POST",
          body: formData,
        });
        if (!res.ok) throw new Error(`Chunk ${i} upload failed`);
        uploadedBytes += chunkBlob.size;
        updateProgress();
      };

      // Worker pool: several chunks in flight for real throughput
      let next = 0;
      const worker = async () => {
        while (next < pending.length && !failed) {
          const i = pending[next++];
          try {
            await uploadChunk(i);
          } catch (err) {
            failed = true;
            throw err;
          }
        }
      };
      const workers = [];
      for (let w = 0; w < Math.min(PARALLEL_CHUNKS, pending.length); w++) workers.push(worker());
      await Promise.all(workers);

      if (failed) throw new Error("分片上传失败，已暂停（重新选择文件可断点续传）");

      // Complete merge
      speedEl.textContent = "正在合并落地...";
      const completeRes = await fetch("/api/upload/complete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          file_id: fileID,
          filename: file.name,
          total_chunks: totalChunks,
        }),
      });

      if (!completeRes.ok) throw new Error("Merge failed");

      localStorage.removeItem("landrop_upload_" + sig);
      speedEl.textContent = "上传完成!";
      setTimeout(() => progressCard.remove(), 2500);
      loadFiles();
    } catch (err) {
      speedEl.textContent = `上传中断: ${err.message}`;
      speedEl.style.color = "var(--danger)";
    }
  }

  // File List & Management
  async function loadFiles() {
    try {
      const res = await fetch("/api/files");
      if (!res.ok) return;
      const data = await res.json();
      fileList = data.files || [];
      renderFileList();
    } catch (e) {
      console.error("Load files error", e);
    }
  }

  function isPreviewable(name) {
    return /\.(png|jpe?g|gif|webp|bmp|svg|mp4|webm|mov|m4v|mp3|wav|ogg|m4a)$/i.test(name);
  }

  function renderFileList() {
    fileCountEl.textContent = `${fileList.length} 个`;
    fileListEl.innerHTML = "";

    if (fileList.length === 0) {
      fileListEl.innerHTML = `<div style="text-align:center; color:var(--text-muted); padding:20px; font-size:13px;">暂无已传输文件</div>`;
      return;
    }

    fileList.forEach((file) => {
      const card = document.createElement("div");
      card.className = "file-card";

      const timeStr = new Date(file.mod_time).toLocaleString();
      const safeURL = escapeHTML(file.url || "");
      const previewable = isPreviewable(file.name);

      card.innerHTML = `
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
      fileListEl.appendChild(card);
    });
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

  async function deleteFile(fileName) {
    if (!confirm(`确定在服务端删除文件 "${fileName}" 吗？`)) return;
    try {
      const res = await fetch("/api/files/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ filename: fileName }),
      });
      if (res.ok) {
        showToast("文件已删除", "info");
        fileList = fileList.filter((f) => f.name !== fileName);
        renderFileList();
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

    // Ask for notification permission after the first interaction
    document.addEventListener("click", requestNotifyPermission, { once: true });
  }

  // Utilities
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
