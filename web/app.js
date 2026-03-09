(function () {
  "use strict";

  // ==================== State ====================
  const state = {
    jars: [],           // discovered JAR files [{name, path, size, checked}]
    outputDir: null,    // last decompile output dir
    tabs: [],
    activeTab: null,
    browseTarget: null, // "source" | "output"
  };

  const $ = (sel) => document.querySelector(sel);
  const $$ = (sel) => document.querySelectorAll(sel);

  const els = {
    sourceDir: $("#source-dir"),
    outputDir: $("#output-dir"),
    btnBrowseSource: $("#btn-browse-source"),
    btnBrowseOutput: $("#btn-browse-output"),
    btnScan: $("#btn-scan"),
    btnDecompile: $("#btn-decompile"),
    btnSettings: $("#btn-settings"),
    btnSelectAll: $("#btn-select-all"),
    jarCount: $("#jar-count"),
    jarList: $("#jar-list"),
    btnSearchToggle: $("#btn-search-toggle"),
    searchPanel: $("#search-panel"),
    searchKeyword: $("#search-keyword"),
    searchIgnoreCase: $("#search-ignore-case"),
    btnSearch: $("#btn-search"),
    searchResults: $("#search-results"),
    fileTree: $("#file-tree"),
    tabBar: $("#tab-bar"),
    codeViewer: $("#code-viewer"),
    dropZone: $("#drop-zone"),
    statusText: $("#status-text"),
    statusJarInfo: $("#status-jar-info"),
    statusFileInfo: $("#status-file-info"),
    loadingOverlay: $("#loading-overlay"),
    loadingText: $("#loading-text"),
    loadingDetail: $("#loading-detail"),
    toastContainer: $("#toast-container"),
    dragOverlay: $("#drag-overlay"),
    modalBrowse: $("#modal-browse"),
    browseTitle: $("#browse-title"),
    browseCurrentDir: $("#browse-current-dir"),
    browseList: $("#browse-list"),
    browseUp: $("#browse-up"),
    browseGo: $("#browse-go"),
    browseSelectDir: $("#browse-select-dir"),
    browseCancel: $("#browse-cancel"),
    modalSettings: $("#modal-settings"),
    settingJavaPath: $("#setting-java-path"),
    settingCfrPath: $("#setting-cfr-path"),
    settingsSave: $("#settings-save"),
  };

  // ==================== API ====================
  async function api(method, url, body) {
    const opts = { method, headers: {} };
    if (body) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(url, opts);
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
    return data;
  }

  // ==================== Toast / Loading / Modal ====================
  function toast(msg, type = "info") {
    const el = document.createElement("div");
    el.className = `toast ${type}`;
    el.textContent = msg;
    els.toastContainer.appendChild(el);
    setTimeout(() => el.remove(), 3000);
  }

  function showLoading(text, detail) {
    els.loadingText.textContent = text;
    els.loadingDetail.textContent = detail || "";
    els.loadingOverlay.classList.remove("hidden");
  }

  function hideLoading() { els.loadingOverlay.classList.add("hidden"); }

  function openModal(m) { m.classList.remove("hidden"); }
  function closeModal(m) { m.classList.add("hidden"); }

  function setupModalClose(modal) {
    modal.querySelectorAll(".modal-close, .modal-close-btn").forEach((b) =>
      b.addEventListener("click", () => closeModal(modal))
    );
    modal.querySelector(".modal-backdrop").addEventListener("click", () => closeModal(modal));
  }

  function setStatus(t) { els.statusText.textContent = t; }

  // ==================== Drag & Drop ====================
  let dragCounter = 0;

  function setupDragDrop() {
    document.addEventListener("dragenter", (e) => {
      e.preventDefault();
      dragCounter++;
      if (dragCounter === 1) els.dragOverlay.classList.remove("hidden");
    });

    document.addEventListener("dragleave", (e) => {
      e.preventDefault();
      dragCounter--;
      if (dragCounter <= 0) {
        dragCounter = 0;
        els.dragOverlay.classList.add("hidden");
      }
    });

    document.addEventListener("dragover", (e) => e.preventDefault());

    document.addEventListener("drop", async (e) => {
      e.preventDefault();
      dragCounter = 0;
      els.dragOverlay.classList.add("hidden");
      await handleDrop(e);
    });
  }

  async function handleDrop(e) {
    const items = e.dataTransfer.items;
    if (!items || items.length === 0) return;

    showLoading("正在读取拖入的文件...", "扫描文件夹内容中");
    const jarFiles = [];

    try {
      for (let i = 0; i < items.length; i++) {
        const entry = items[i].webkitGetAsEntry ? items[i].webkitGetAsEntry() : null;
        if (entry) {
          await collectJarEntries(entry, jarFiles);
        } else {
          const file = e.dataTransfer.files[i];
          if (file && file.name.toLowerCase().endsWith(".jar")) {
            jarFiles.push(file);
          }
        }
      }

      if (jarFiles.length === 0) {
        hideLoading();
        toast("未发现任何 JAR 文件", "error");
        return;
      }

      els.loadingText.textContent = `发现 ${jarFiles.length} 个 JAR 文件，正在上传...`;

      const formData = new FormData();
      for (const f of jarFiles) {
        formData.append("files", f);
      }

      const res = await fetch("/api/upload", { method: "POST", body: formData });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error);

      hideLoading();

      if (data.files && data.files.length > 0) {
        els.sourceDir.value = data.tempDir;
        setJarList(data.files);
        toast(`已识别 ${data.files.length} 个 JAR 文件`, "success");
        setStatus(`已发现 ${data.files.length} 个 JAR 文件 — 点击「反编译」开始`);
      } else {
        toast("上传的文件中没有有效的 JAR", "error");
      }
    } catch (err) {
      hideLoading();
      toast("处理拖入文件失败: " + err.message, "error");
    }
  }

  async function collectJarEntries(entry, results) {
    if (entry.isFile) {
      if (entry.name.toLowerCase().endsWith(".jar")) {
        const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
        results.push(file);
      }
    } else if (entry.isDirectory) {
      const entries = await readAllEntries(entry.createReader());
      for (const e of entries) {
        await collectJarEntries(e, results);
      }
    }
  }

  function readAllEntries(reader) {
    return new Promise((resolve, reject) => {
      const all = [];
      function readBatch() {
        reader.readEntries((batch) => {
          if (batch.length === 0) { resolve(all); }
          else { all.push(...batch); readBatch(); }
        }, reject);
      }
      readBatch();
    });
  }

  // ==================== JAR List ====================
  function setJarList(jars) {
    state.jars = jars.map((j) => ({ ...j, checked: true }));
    renderJarList();
    els.btnDecompile.disabled = false;
  }

  function renderJarList() {
    els.jarList.innerHTML = "";
    const checkedCount = state.jars.filter((j) => j.checked).length;
    els.jarCount.textContent = `${checkedCount}/${state.jars.length}`;

    if (state.jars.length === 0) {
      els.jarList.innerHTML = '<div class="empty-state small-empty"><p>拖拽文件夹到页面或输入路径扫描</p></div>';
      els.btnDecompile.disabled = true;
      return;
    }

    for (let i = 0; i < state.jars.length; i++) {
      const j = state.jars[i];
      const item = document.createElement("div");
      item.className = "jar-item";

      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = j.checked;
      cb.addEventListener("change", () => {
        state.jars[i].checked = cb.checked;
        const c = state.jars.filter((x) => x.checked).length;
        els.jarCount.textContent = `${c}/${state.jars.length}`;
        els.btnDecompile.disabled = c === 0;
      });

      const icon = document.createElement("span");
      icon.className = "jar-item-icon";
      icon.textContent = "☕";

      const info = document.createElement("div");
      info.className = "jar-item-info";

      const name = document.createElement("div");
      name.className = "jar-item-name";
      name.textContent = j.name;

      const path = document.createElement("div");
      path.className = "jar-item-path";
      path.textContent = j.path;
      path.title = j.path;

      info.appendChild(name);
      info.appendChild(path);

      const size = document.createElement("span");
      size.className = "jar-item-size";
      size.textContent = formatSize(j.size);

      item.appendChild(cb);
      item.appendChild(icon);
      item.appendChild(info);
      item.appendChild(size);
      els.jarList.appendChild(item);
    }
  }

  // ==================== Scan ====================
  async function scanDir() {
    const dir = els.sourceDir.value.trim();
    if (!dir) {
      toast("请输入源目录路径", "error");
      return;
    }

    showLoading("正在扫描目录...", dir);
    setStatus("扫描中...");

    try {
      const data = await api("POST", "/api/scan", { dir });
      hideLoading();

      if (!data.jars || data.jars.length === 0) {
        toast("该目录中没有找到 JAR 文件", "error");
        setStatus("未找到 JAR 文件");
        return;
      }

      setJarList(data.jars);
      toast(`发现 ${data.total} 个 JAR 文件`, "success");
      setStatus(`发现 ${data.total} 个 JAR 文件 — 点击「反编译」开始`);
      els.statusJarInfo.textContent = dir;
    } catch (e) {
      hideLoading();
      toast("扫描失败: " + e.message, "error");
      setStatus("扫描失败");
    }
  }

  // ==================== Decompile ====================
  async function decompileAll() {
    const selected = state.jars.filter((j) => j.checked);
    if (selected.length === 0) {
      toast("请至少选择一个 JAR 文件", "error");
      return;
    }

    let outDir = els.outputDir.value.trim();
    if (!outDir) {
      const srcDir = els.sourceDir.value.trim();
      if (srcDir) {
        outDir = srcDir.replace(/\/$/, "") + "_decompiled";
      } else {
        outDir = "/tmp/jar-fucker-output";
      }
      els.outputDir.value = outDir;
    }

    showLoading(`正在反编译 ${selected.length} 个 JAR 文件...`, "这可能需要一些时间");
    setStatus("反编译中...");

    try {
      const jarPaths = selected.map((j) => j.path);
      const result = await api("POST", "/api/decompile", {
        jars: jarPaths,
        outputDir: outDir,
      });

      state.outputDir = result.outputDir;
      hideLoading();

      toast(`反编译完成！${result.javaFiles} 个 Java 文件，耗时 ${result.elapsed}`, "success");
      setStatus(`反编译完成 | ${result.javaFiles} 个文件 | ${result.elapsed}`);
      els.statusJarInfo.textContent = `输出: ${result.outputDir}`;

      await loadFileTree(result.outputDir);
    } catch (e) {
      hideLoading();
      toast("反编译失败: " + e.message, "error");
      setStatus("反编译失败");
    }
  }

  // ==================== File Tree ====================
  async function loadFileTree(rootDir) {
    try {
      const tree = await api("GET", `/api/tree?root=${encodeURIComponent(rootDir)}`);
      els.fileTree.innerHTML = "";
      if (tree.children && tree.children.length > 0) {
        renderTreeNodes(els.fileTree, tree.children, rootDir, 0);
      } else {
        els.fileTree.innerHTML = '<div class="empty-state small-empty"><p>目录为空</p></div>';
      }
    } catch (e) {
      toast("加载文件树失败: " + e.message, "error");
    }
  }

  function renderTreeNodes(container, nodes, rootDir, depth) {
    for (const node of nodes) {
      const item = document.createElement("div");
      item.className = "tree-item";
      item.style.paddingLeft = 8 + depth * 16 + "px";

      if (node.type === "dir") {
        const toggle = document.createElement("span");
        toggle.className = "tree-toggle expanded";
        toggle.innerHTML = "▶";
        item.appendChild(toggle);

        const icon = document.createElement("span");
        icon.className = "tree-icon";
        icon.textContent = "📂";
        item.appendChild(icon);

        const name = document.createElement("span");
        name.className = "tree-name";
        name.textContent = node.name;
        item.appendChild(name);

        const childContainer = document.createElement("div");

        item.addEventListener("click", (e) => {
          e.stopPropagation();
          const exp = toggle.classList.toggle("expanded");
          childContainer.style.display = exp ? "block" : "none";
          icon.textContent = exp ? "📂" : "📁";
        });

        container.appendChild(item);
        if (node.children && node.children.length > 0) {
          renderTreeNodes(childContainer, node.children, rootDir, depth + 1);
        }
        container.appendChild(childContainer);
      } else {
        const spacer = document.createElement("span");
        spacer.className = "tree-indent";
        item.appendChild(spacer);

        const icon = document.createElement("span");
        icon.className = "tree-icon";
        icon.textContent = getFileIcon(node.name);
        item.appendChild(icon);

        const name = document.createElement("span");
        name.className = "tree-name";
        name.textContent = node.name;
        item.appendChild(name);

        const fullPath = rootDir + "/" + node.path;
        item.addEventListener("click", (e) => {
          e.stopPropagation();
          openFile(fullPath, node.name);
        });
        container.appendChild(item);
      }
    }
  }

  function getFileIcon(name) {
    const ext = name.split(".").pop().toLowerCase();
    return { java: "☕", xml: "📋", properties: "⚙", mf: "📋", txt: "📝", json: "📦", class: "⚙" }[ext] || "📄";
  }

  // ==================== Tabs & Code Viewer ====================
  async function openFile(path, name) {
    const existing = state.tabs.find((t) => t.path === path);
    if (existing) { activateTab(existing.id); return; }

    try {
      setStatus(`加载 ${name}...`);
      const data = await api("GET", `/api/file?path=${encodeURIComponent(path)}`);
      const tabId = "tab-" + Date.now();
      state.tabs.push({ id: tabId, path, name, content: data.content, size: data.size });
      renderTabBar();
      activateTab(tabId);
      els.statusFileInfo.textContent = `${name} | ${formatSize(data.size)}`;
    } catch (e) {
      toast("打开文件失败: " + e.message, "error");
    }
  }

  function renderTabBar() {
    els.tabBar.innerHTML = "";
    for (const tab of state.tabs) {
      const el = document.createElement("div");
      el.className = "tab" + (tab.id === state.activeTab ? " active" : "");
      el.innerHTML = `<span style="font-size:12px">${getFileIcon(tab.name)}</span><span>${tab.name}</span>`;

      const close = document.createElement("span");
      close.className = "tab-close";
      close.textContent = "×";
      close.addEventListener("click", (e) => { e.stopPropagation(); closeTab(tab.id); });
      el.appendChild(close);

      el.addEventListener("click", () => activateTab(tab.id));
      els.tabBar.appendChild(el);
    }
  }

  function activateTab(tabId) {
    state.activeTab = tabId;
    const tab = state.tabs.find((t) => t.id === tabId);
    if (!tab) return;
    renderTabBar();
    renderCode(tab.content, tab.name);
  }

  function closeTab(tabId) {
    const idx = state.tabs.findIndex((t) => t.id === tabId);
    if (idx === -1) return;
    state.tabs.splice(idx, 1);
    if (state.activeTab === tabId) {
      if (state.tabs.length > 0) {
        activateTab(state.tabs[Math.min(idx, state.tabs.length - 1)].id);
      } else {
        state.activeTab = null;
        showWelcome();
      }
    }
    renderTabBar();
  }

  function renderCode(content, filename) {
    const ext = filename.split(".").pop().toLowerCase();
    const lang = { java: "java", xml: "xml", properties: "properties" }[ext] || "plaintext";
    const lines = content.split("\n");
    const lineNums = lines.map((_, i) => i + 1).join("\n");

    let highlighted;
    try {
      highlighted = hljs.highlight(content, { language: lang, ignoreIllegals: true }).value;
    } catch { highlighted = escapeHtml(content); }

    els.codeViewer.innerHTML = `
      <div class="code-container">
        <div class="line-numbers">${lineNums}</div>
        <div class="code-content"><pre><code class="hljs">${highlighted}</code></pre></div>
      </div>`;
  }

  function showWelcome() {
    els.codeViewer.innerHTML = `
      <div id="drop-zone" class="welcome-screen">
        <div class="drop-zone-inner">
          <div class="drop-icon">📂</div>
          <h1>拖拽文件夹到此处</h1>
          <p>自动识别文件夹内所有 JAR 包</p>
          <div class="drop-divider"><span>或</span></div>
          <div class="welcome-steps">
            <div class="step"><div class="step-num">1</div><div class="step-text">在顶部输入文件夹路径，点击「扫描」</div></div>
            <div class="step"><div class="step-num">2</div><div class="step-text">确认 JAR 列表后，点击「反编译」</div></div>
            <div class="step"><div class="step-num">3</div><div class="step-text">在左侧浏览反编译的 Java 源码</div></div>
          </div>
          <div class="welcome-shortcut">支持拖拽 <kbd>.jar</kbd> 文件 或 包含 JAR 的文件夹</div>
        </div>
      </div>`;
  }

  // ==================== Search ====================
  async function doSearch() {
    const keyword = els.searchKeyword.value.trim();
    if (!keyword || !state.outputDir) {
      toast(state.outputDir ? "请输入搜索关键词" : "请先反编译", "error");
      return;
    }

    try {
      setStatus("搜索中...");
      const data = await api("POST", "/api/search", {
        dir: state.outputDir, keyword,
        ignoreCase: els.searchIgnoreCase.checked,
        maxResults: 100,
      });

      els.searchResults.innerHTML = "";
      if (!data.results || data.results.length === 0) {
        els.searchResults.innerHTML = '<div class="empty-state small-empty"><p>无结果</p></div>';
        setStatus("搜索完成，无结果");
        return;
      }

      for (const r of data.results) {
        const item = document.createElement("div");
        item.className = "search-result-item";
        item.innerHTML = `
          <div class="search-result-file">${r.file}</div>
          <div class="search-result-content">${highlightKw(escapeHtml(r.content.trim()), keyword, els.searchIgnoreCase.checked)}</div>
          <div class="search-result-line">第 ${r.line} 行</div>`;
        item.addEventListener("click", () => openFile(state.outputDir + "/" + r.file, r.file.split("/").pop()));
        els.searchResults.appendChild(item);
      }
      setStatus(`找到 ${data.total} 处匹配`);
    } catch (e) {
      toast("搜索失败: " + e.message, "error");
    }
  }

  function highlightKw(html, kw, ignoreCase) {
    const re = new RegExp(`(${kw.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")})`, ignoreCase ? "gi" : "g");
    return html.replace(re, "<mark>$1</mark>");
  }

  // ==================== File Browser ====================
  async function browseTo(dir) {
    try {
      const data = await api("GET", `/api/browse?dir=${encodeURIComponent(dir)}`);
      els.browseCurrentDir.value = data.dir;
      els.browseList.innerHTML = "";

      for (const entry of data.entries) {
        const item = document.createElement("div");
        item.className = "browse-item";
        item.innerHTML = `
          <span class="browse-item-icon">${entry.type === "dir" ? "📁" : entry.isJar ? "☕" : "📄"}</span>
          <span class="browse-item-name ${entry.isJar ? "browse-item-jar" : ""}">${entry.name}</span>
          ${entry.size ? `<span class="browse-item-size">${formatSize(entry.size)}</span>` : ""}`;

        item.addEventListener("dblclick", () => {
          if (entry.type === "dir") browseTo(entry.path);
        });
        item.addEventListener("click", () => {
          if (entry.type === "dir") browseTo(entry.path);
        });

        els.browseList.appendChild(item);
      }
    } catch (e) {
      toast("浏览失败: " + e.message, "error");
    }
  }

  // ==================== Settings ====================
  async function loadConfig() {
    try {
      const c = await api("GET", "/api/config");
      els.settingJavaPath.placeholder = c.javaPath || "未找到";
      els.settingCfrPath.placeholder = c.cfrPath || "自动下载";
    } catch { /* ignore */ }
  }

  async function saveSettings() {
    const jp = els.settingJavaPath.value.trim();
    const cp = els.settingCfrPath.value.trim();
    if (jp || cp) {
      try { await api("PUT", "/api/config", { javaPath: jp, cfrPath: cp }); }
      catch (e) { toast("保存失败: " + e.message, "error"); return; }
    }
    closeModal(els.modalSettings);
    toast("设置已保存", "success");
  }

  // ==================== Resizer ====================
  function setupResizer() {
    const resizer = $("#resizer");
    const sidebar = $("#sidebar");
    let startX, startW;
    resizer.addEventListener("mousedown", (e) => {
      startX = e.clientX; startW = sidebar.offsetWidth;
      resizer.classList.add("dragging");
      const move = (e) => {
        const w = startW + (e.clientX - startX);
        if (w >= 150 && w <= 600) sidebar.style.width = w + "px";
      };
      const up = () => {
        resizer.classList.remove("dragging");
        document.removeEventListener("mousemove", move);
        document.removeEventListener("mouseup", up);
      };
      document.addEventListener("mousemove", move);
      document.addEventListener("mouseup", up);
    });
  }

  // ==================== Utilities ====================
  function formatSize(bytes) {
    const u = ["B", "KB", "MB", "GB"];
    let i = 0, s = bytes;
    while (s >= 1024 && i < u.length - 1) { s /= 1024; i++; }
    return s.toFixed(i === 0 ? 0 : 1) + " " + u[i];
  }

  function escapeHtml(str) {
    const d = document.createElement("div");
    d.textContent = str;
    return d.innerHTML;
  }

  // ==================== Init ====================
  function init() {
    setupModalClose(els.modalBrowse);
    setupModalClose(els.modalSettings);
    setupDragDrop();
    setupResizer();

    // Browse buttons
    els.btnBrowseSource.addEventListener("click", () => {
      state.browseTarget = "source";
      els.browseTitle.textContent = "选择源目录 (包含 JAR 的文件夹)";
      browseTo(els.sourceDir.value.trim() || "/");
      openModal(els.modalBrowse);
    });

    els.btnBrowseOutput.addEventListener("click", () => {
      state.browseTarget = "output";
      els.browseTitle.textContent = "选择输出目录";
      browseTo(els.outputDir.value.trim() || "/");
      openModal(els.modalBrowse);
    });

    els.browseUp.addEventListener("click", () => {
      const cur = els.browseCurrentDir.value;
      browseTo(cur.substring(0, cur.lastIndexOf("/")) || "/");
    });

    els.browseGo.addEventListener("click", () => browseTo(els.browseCurrentDir.value));
    els.browseCurrentDir.addEventListener("keydown", (e) => { if (e.key === "Enter") browseTo(els.browseCurrentDir.value); });

    els.browseSelectDir.addEventListener("click", () => {
      const dir = els.browseCurrentDir.value;
      if (state.browseTarget === "source") els.sourceDir.value = dir;
      else els.outputDir.value = dir;
      closeModal(els.modalBrowse);
    });

    els.browseCancel.addEventListener("click", () => closeModal(els.modalBrowse));

    // Actions
    els.btnScan.addEventListener("click", scanDir);
    els.btnDecompile.addEventListener("click", decompileAll);

    els.btnSelectAll.addEventListener("click", () => {
      const allChecked = state.jars.every((j) => j.checked);
      state.jars.forEach((j) => (j.checked = !allChecked));
      renderJarList();
    });

    // Settings
    els.btnSettings.addEventListener("click", () => { loadConfig(); openModal(els.modalSettings); });
    els.settingsSave.addEventListener("click", saveSettings);

    // Search
    els.btnSearchToggle.addEventListener("click", () => {
      els.searchPanel.classList.toggle("hidden");
      if (!els.searchPanel.classList.contains("hidden")) els.searchKeyword.focus();
    });
    els.btnSearch.addEventListener("click", doSearch);
    els.searchKeyword.addEventListener("keydown", (e) => { if (e.key === "Enter") doSearch(); });

    // Keyboard shortcuts
    document.addEventListener("keydown", (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "f") {
        e.preventDefault();
        els.btnSearchToggle.click();
        els.searchKeyword.focus();
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault();
        if (!els.btnDecompile.disabled) decompileAll();
        else scanDir();
      }
    });

    els.sourceDir.addEventListener("keydown", (e) => { if (e.key === "Enter") scanDir(); });

    loadConfig();
  }

  document.addEventListener("DOMContentLoaded", init);
})();
