// LAN Drop Frontend Core Engine
(function () {
  const CHUNK_SIZE = 4 * 1024 * 1024; // 4MB per chunk

  // State
  let ws = null;
  let wsConnected = false;
  let textFeed = [];
  let fileList = [];
  let deviceName = getOrCreateDeviceName();

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

  // Init
  init();

  async function init() {
    setupEventListeners();
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
      const res = await fetch("/api/info");
      if (res.status === 401) {
        showPinModal();
        return;
      }
      const data = await res.json();
      serverNameEl.textContent = `${data.hostname || "LAN-Server"} (${data.host_ip}:${data.port})`;
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
        init();
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
      }
    } else if (msg.type === "new_file") {
      fileList.unshift(msg.data);
      renderFileList();
      showToast(`新文件到达: ${msg.data.name}`, "success");
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

  // File Upload & Chunking Engine
  async function handleFiles(files) {
    for (const file of files) {
      await uploadFileInChunks(file);
    }
  }

  async function uploadFileInChunks(file) {
    const fileID = `${Date.now()}_${Math.random().toString(36).substr(2, 9)}`;
    const totalChunks = Math.ceil(file.size / CHUNK_SIZE);

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

    try {
      for (let i = 0; i < totalChunks; i++) {
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

        if (!res.ok) throw new Error("Chunk upload failed");

        uploadedBytes += chunkBlob.size;
        const percent = Math.round((uploadedBytes / file.size) * 100);
        fillEl.style.width = `${percent}%`;
        pctEl.textContent = `${percent}%`;

        // Calculate speed
        const elapsedSec = (Date.now() - startTime) / 1000;
        if (elapsedSec > 0) {
          const speed = uploadedBytes / elapsedSec;
          speedEl.textContent = `${formatBytes(speed)}/s`;
        }
      }

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

      card.innerHTML = `
        <div class="file-info-group">
          <div class="file-icon">📄</div>
          <div class="file-details">
            <div class="file-name" title="${escapeHTML(file.name)}">${escapeHTML(file.name)}</div>
            <div class="file-meta">${formatBytes(file.size)} • ${timeStr}</div>
          </div>
        </div>
        <div class="file-actions">
          <a href="${file.url}" download="${escapeHTML(file.name)}" class="btn-primary btn-sm" style="text-decoration:none;">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
            下载
          </a>
          <button class="btn-danger btn-sm btn-del">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
          </button>
        </div>
      `;

      card.querySelector(".btn-del").addEventListener("click", () => deleteFile(file.name));
      fileListEl.appendChild(card);
    });
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

  // Event Listeners
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
  }

  // Utilities
  function formatBytes(bytes) {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
  }

  function escapeHTML(str) {
    if (!str) return "";
    return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function escapeAndFormatUrls(str) {
    if (!str) return "";
    const escaped = escapeHTML(str);
    const urlRegex = /(https?:\/\/[^\s]+)/g;
    return escaped.replace(urlRegex, (url) => `<a href="${url}" target="_blank" rel="noopener noreferrer">${url}</a>`);
  }
})();
