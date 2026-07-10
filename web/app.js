(function () {
  "use strict";

  const TOKEN_KEY = "jar-fucker-launch-token";
  const TOKEN_CHANNEL = "jar-fucker-session-v1";
  const SESSION_EXPIRED_MESSAGE =
    "服务会话已更新，请使用程序打开的最新启动页重新连接";
  const CODE_LINE_HEIGHT = 21;
  const CODE_TOP_PADDING = 15;
  const TREE_PAGE_SIZE = 300;
  const MAX_TREE_ROWS = 5000;
  const MAX_PACKAGE_ROWS = 250;
  const MAX_HIGHLIGHT_CHARS = 1 << 20;
  const MAX_NUMBERED_LINES = 20000;
  const MAX_SEARCH_MARKS = 20;
  const LAYOUT_STORAGE_KEY = "jar-fucker-layout-v1";
  const RESIZE_KEYBOARD_STEP = 16;

  let launchToken = readStoredLaunchToken();
  let tokenChannel = null;

  initializeTokenChannel();
  captureLaunchToken();
  window.addEventListener("hashchange", captureLaunchToken);

  const state = {
    jars: [],
    scanSession: null,
    outputDir: null,
    resultSessionId: 0,
    mode: "decompile",
    activeJarPath: null,
    analysisCache: new Map(),
    analysisController: null,
    tabs: [],
    activeTab: null,
    tabSequence: 0,
    pendingFiles: new Map(),
    browseTarget: null,
    browseParent: null,
    activeTask: null,
    lastTaskLog: "",
    searchController: null,
    treeController: null,
    sessionExpired: false,
    dialogReturnFocus: new WeakMap(),
  };

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) =>
    Array.from(root.querySelectorAll(selector));

  const els = {};

  function isLaunchToken(token) {
    return /^[A-Za-z0-9_-]{43}$/.test(String(token || ""));
  }

  function readStoredLaunchToken() {
    try {
      const token = sessionStorage.getItem(TOKEN_KEY) || "";
      return isLaunchToken(token) ? token : "";
    } catch {
      return "";
    }
  }

  function storeLaunchToken(token) {
    launchToken = token;
    try {
      sessionStorage.setItem(TOKEN_KEY, token);
    } catch {
      // The in-memory token remains available in privacy-restricted browsers.
    }
  }

  function clearLaunchToken(expectedToken) {
    if (expectedToken !== launchToken) return false;
    launchToken = "";
    try {
      if (sessionStorage.getItem(TOKEN_KEY) === expectedToken)
        sessionStorage.removeItem(TOKEN_KEY);
    } catch {
      // The in-memory token has already been cleared.
    }
    return true;
  }

  function initializeTokenChannel() {
    if (typeof window.BroadcastChannel !== "function") return;
    try {
      tokenChannel = new BroadcastChannel(TOKEN_CHANNEL);
      tokenChannel.addEventListener("message", (event) => {
        const token = String(event.data?.token || "");
        if (!isLaunchToken(token) || token === launchToken) return;
        void verifyAndInstallBroadcastToken(token);
      });
    } catch {
      tokenChannel = null;
    }
  }

  async function verifyAndInstallBroadcastToken(token) {
    try {
      const response = await fetch("/api/session", {
        method: "GET",
        headers: { "X-Jar-Fucker-Token": token },
        cache: "no-store",
        credentials: "same-origin",
      });
      if (!response.ok || !isLaunchToken(token)) return;
      storeLaunchToken(token);
      handleSessionRecovered();
    } catch {
      // A restarting service may not be ready when the first broadcast arrives.
    }
  }

  function captureLaunchToken() {
    if (!location.hash) return;
    const params = new URLSearchParams(location.hash.slice(1));
    if (!params.has("token")) return;

    const token = params.get("token") || "";
    if (isLaunchToken(token)) {
      storeLaunchToken(token);
      if (tokenChannel) tokenChannel.postMessage({ token });
    }

    history.replaceState(null, "", `${location.pathname}${location.search}`);
  }

  function getLaunchToken() {
    return launchToken;
  }

  function handleSessionRecovered() {
    const wasExpired = state.sessionExpired;
    state.sessionExpired = false;
    if (!els.sourceDir) return;
    setControlsBusy(Boolean(state.activeTask));
    setStatus("已连接到新的服务会话");
    if (wasExpired) toast("服务连接已恢复，请重试刚才的操作", "success");
  }

  function handleUnauthorized(tokenUsed) {
    if (!clearLaunchToken(tokenUsed)) return;
    const firstNotice = !state.sessionExpired;
    state.sessionExpired = true;
    if (!els.sourceDir) return;
    setControlsBusy(true);
    setStatus("服务会话已失效", "等待新的启动页连接");
    if (firstNotice) toast(SESSION_EXPIRED_MESSAGE, "error");
  }

  async function apiFetch(url, options = {}) {
    const headers = new Headers(options.headers || {});
    const tokenUsed = getLaunchToken();
    if (tokenUsed && url.startsWith("/api")) {
      headers.set("X-Jar-Fucker-Token", tokenUsed);
    }
    const response = await fetch(url, { ...options, headers });
    if (response.status === 401 && url.startsWith("/api"))
      handleUnauthorized(tokenUsed);
    return response;
  }

  async function api(method, url, body, options = {}) {
    const headers = new Headers(options.headers || {});
    const request = { method, headers, signal: options.signal };
    if (body !== undefined && body !== null) {
      headers.set("Content-Type", "application/json");
      request.body = JSON.stringify(body);
    }

    const response = await apiFetch(url, request);
    const data = await readResponseData(response);
    if (!response.ok) {
      throw taskError(apiErrorMessage(response, data), data.log || "");
    }
    return data;
  }

  async function readResponseData(response) {
    const raw = await response.text();
    if (!raw) return {};
    try {
      return JSON.parse(raw);
    } catch {
      return { message: raw };
    }
  }

  function apiErrorMessage(response, data) {
    if (response.status === 401) return SESSION_EXPIRED_MESSAGE;
    return data.error || data.message || `HTTP ${response.status}`;
  }

  function taskError(message, log) {
    const error = new Error(message || "请求失败");
    error.log = log || "";
    return error;
  }

  function create(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function createIcon(name) {
    const node = document.createElement("i");
    node.dataset.lucide = name;
    node.setAttribute("aria-hidden", "true");
    return node;
  }

  function refreshIcons() {
    if (window.lucide && typeof window.lucide.createIcons === "function") {
      if (!document.querySelector("i[data-lucide]")) return;
      window.lucide.createIcons({ attrs: { "aria-hidden": "true" } });
      document
        .querySelectorAll("svg[data-lucide]")
        .forEach((icon) => icon.removeAttribute("data-lucide"));
    }
  }

  function toast(message, type = "info") {
    const item = create("div", `toast ${type}`, message);
    item.setAttribute("role", type === "error" ? "alert" : "status");
    els.toastContainer.appendChild(item);
    window.setTimeout(() => item.remove(), type === "error" ? 6500 : 4200);
  }

  function setStatus(message, context) {
    els.statusText.textContent = message || "";
    if (context !== undefined) els.statusContext.textContent = context || "";
  }

  function isAbortError(error) {
    return error && error.name === "AbortError";
  }

  function formatSize(bytes) {
    const value = Number(bytes);
    if (!Number.isFinite(value) || value < 0) return "-";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let size = value;
    let unit = 0;
    while (size >= 1024 && unit < units.length - 1) {
      size /= 1024;
      unit += 1;
    }
    return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
  }

  function timestampForPath() {
    const date = new Date();
    const pad = (value) => String(value).padStart(2, "0");
    return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}-${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`;
  }

  function buildDefaultOutputDir(sourceDir, mode = "decompile") {
    const source = sourceDir.trim();
    const separator =
      source.includes("\\") && !source.includes("/") ? "\\" : "/";
    const suffix = mode === "extract" ? "extracted" : "decompiled";
    if (/^[A-Za-z]:[\\/]$/.test(source) || source === "/") {
      return `${source}jar-fucker_${suffix}${separator}${timestampForPath()}`;
    }
    const base = source.replace(/[\\/]+$/, "");
    return `${base}_${suffix}${separator}${timestampForPath()}`;
  }

  function joinServerPath(root, relativePath) {
    const separator = root.includes("\\") ? "\\" : "/";
    const normalizedRelative = String(relativePath || "")
      .replace(/[\\/]+/g, separator)
      .replace(/^[\\/]+/, "");
    if (!normalizedRelative) return root;
    return /[\\/]$/.test(root)
      ? `${root}${normalizedRelative}`
      : `${root}${separator}${normalizedRelative}`;
  }

  function fileNameFromPath(path) {
    const parts = String(path || "").split(/[\\/]/);
    return parts[parts.length - 1] || path;
  }

  function fileIconName(name) {
    const extension = String(name || "")
      .split(".")
      .pop()
      .toLowerCase();
    return (
      {
        java: "file-code-2",
        class: "file-cog",
        xml: "file-code",
        json: "file-json",
        properties: "file-sliders",
        mf: "file-text",
        txt: "file-text",
      }[extension] || "file"
    );
  }

  function openDialog(dialog, focusSelector) {
    if (!dialog || dialog.open) return;
    state.dialogReturnFocus.set(dialog, document.activeElement);
    dialog.showModal();
    window.setTimeout(() => {
      const target = focusSelector
        ? $(focusSelector, dialog)
        : $("button, input, [tabindex]:not([tabindex='-1'])", dialog);
      if (target) target.focus();
    }, 0);
  }

  function closeDialog(dialog) {
    if (dialog && dialog.open) dialog.close();
  }

  function setupDialog(dialog) {
    $$(".dialog-close", dialog).forEach((button) => {
      button.addEventListener("click", () => closeDialog(dialog));
    });
    dialog.addEventListener("close", () => {
      const returnTarget = state.dialogReturnFocus.get(dialog);
      if (
        returnTarget &&
        returnTarget.isConnected &&
        typeof returnTarget.focus === "function"
      ) {
        returnTarget.focus();
      }
      state.dialogReturnFocus.delete(dialog);
    });
  }

  function setSidebarOpen(open) {
    document.body.classList.toggle("sidebar-open", open);
    els.btnSidebar.setAttribute("aria-expanded", String(open));
    els.sidebarBackdrop.tabIndex = open ? 0 : -1;
    if (open) {
      window.setTimeout(() => {
        const target = $(
          "button:not(:disabled), input:not(:disabled)",
          els.sidebar,
        );
        if (target) target.focus();
      }, 0);
    }
  }

  function selectedJars() {
    return state.jars.filter((jar) => jar.checked);
  }

  function setControlsBusy(busy) {
    const locked = busy || state.sessionExpired;
    els.sourceDir.disabled = locked;
    els.btnBrowseSource.disabled = locked;
    els.btnScan.disabled = locked;
    els.outputDir.disabled = locked;
    els.btnBrowseOutput.disabled = locked;
    els.filterPackage.disabled = locked;
    els.modeDecompile.disabled = locked;
    els.modeExtract.disabled = locked;
    els.btnSettings.disabled = locked;
    els.btnSelectAll.disabled = locked || state.jars.length === 0;
    els.btnRun.disabled = locked || selectedJars().length === 0;
    $$("input[type='checkbox']", els.jarList).forEach((input) => {
      input.disabled = locked;
    });
  }

  function startTask(label, options = {}) {
    if (state.activeTask) {
      toast(`${state.activeTask.label}仍在进行中`, "error");
      return null;
    }

    const controller = new AbortController();
    state.activeTask = { label, controller };
    const progressIcon = $(".task-progress-icon", els.taskProgress);
    progressIcon.replaceChildren(createIcon("loader-circle"));
    refreshIcons();
    els.taskProgress.hidden = false;
    els.taskProgress.dataset.running = "true";
    els.taskProgress.classList.remove("is-error", "is-success");
    els.taskProgressTitle.textContent = label;
    els.taskProgressDetail.textContent = options.detail || "";
    els.taskProgressPercent.textContent = "";
    els.btnTaskCancel.hidden = false;
    els.btnTaskCancel.disabled = false;
    els.btnTaskDismiss.hidden = true;

    if (Number.isFinite(options.percent)) {
      els.taskProgressBar.value = Math.max(0, Math.min(100, options.percent));
      els.taskProgressPercent.textContent = `${Math.round(options.percent)}%`;
    } else {
      els.taskProgressBar.removeAttribute("value");
    }

    setControlsBusy(true);
    setStatus(label);
    return controller;
  }

  function updateTask(controller, options = {}) {
    if (!state.activeTask || state.activeTask.controller !== controller) return;
    if (options.title) els.taskProgressTitle.textContent = options.title;
    if (options.detail !== undefined)
      els.taskProgressDetail.textContent = options.detail || "";
    if (Number.isFinite(options.percent)) {
      const percent = Math.max(0, Math.min(100, Math.round(options.percent)));
      els.taskProgressBar.value = percent;
      els.taskProgressPercent.textContent = `${percent}%`;
    } else if (options.indeterminate) {
      els.taskProgressBar.removeAttribute("value");
      els.taskProgressPercent.textContent = "";
    }
  }

  function settleTask(controller, outcome, title, detail, percent) {
    if (!state.activeTask || state.activeTask.controller !== controller) return;
    state.activeTask = null;
    els.taskProgress.dataset.running = "false";
    els.taskProgress.classList.toggle("is-error", outcome === "error");
    els.taskProgress.classList.toggle("is-success", outcome === "success");
    const progressIcon = $(".task-progress-icon", els.taskProgress);
    progressIcon.replaceChildren(
      createIcon(
        outcome === "success"
          ? "circle-check"
          : outcome === "error"
            ? "circle-alert"
            : "circle-minus",
      ),
    );
    refreshIcons();
    els.taskProgressTitle.textContent = title;
    els.taskProgressDetail.textContent = detail || "";
    els.btnTaskCancel.hidden = true;
    els.btnTaskDismiss.hidden = false;
    if (Number.isFinite(percent)) {
      els.taskProgressBar.value = percent;
      els.taskProgressPercent.textContent = `${Math.round(percent)}%`;
    } else {
      els.taskProgressBar.removeAttribute("value");
      els.taskProgressPercent.textContent = "";
    }
    setControlsBusy(false);
  }

  function cancelActiveTask() {
    if (!state.activeTask) return false;
    const { controller } = state.activeTask;
    if (!controller.signal.aborted) controller.abort();
    els.btnTaskCancel.disabled = true;
    updateTask(controller, {
      title: "正在取消",
      detail: "等待当前操作停止",
      indeterminate: true,
    });
    return true;
  }

  function saveTaskLog(log) {
    state.lastTaskLog = String(log || "").trim();
    els.btnTaskLog.disabled = !state.lastTaskLog;
    els.taskLogContent.textContent = state.lastTaskLog || "暂无任务日志";
  }

  function showTaskError(controller, prefix, error) {
    if (error.log) saveTaskLog(error.log);
    settleTask(controller, "error", prefix, error.message || "任务失败");
    setStatus(prefix, error.message || "");
    toast(`${prefix}: ${error.message}`, "error");
  }

  function updateSelectionState() {
    const checked = selectedJars().length;
    els.jarCount.textContent = `${checked} / ${state.jars.length}`;
    els.jarCount.setAttribute(
      "aria-label",
      `已选择 ${checked} 个，共 ${state.jars.length} 个`,
    );
    els.btnSelectAll.disabled =
      state.sessionExpired ||
      Boolean(state.activeTask) ||
      state.jars.length === 0;
    els.btnSelectAll.setAttribute(
      "aria-label",
      checked === state.jars.length && checked > 0
        ? "取消全选 JAR"
        : "全选 JAR",
    );
    els.btnSelectAll.title =
      checked === state.jars.length && checked > 0 ? "取消全选" : "全选";
    els.btnRun.disabled =
      state.sessionExpired || Boolean(state.activeTask) || checked === 0;
  }

  function renderJarList() {
    els.jarList.replaceChildren();
    els.queueEmpty.hidden = state.jars.length > 0;

    state.jars.forEach((jar) => {
      const item = create(
        "li",
        `jar-row${jar.path === state.activeJarPath ? " is-active" : ""}`,
      );

      const checkbox = create("input");
      checkbox.type = "checkbox";
      checkbox.checked = jar.checked;
      checkbox.disabled = state.sessionExpired || Boolean(state.activeTask);
      checkbox.setAttribute("aria-label", `选择 ${jar.name}`);
      checkbox.addEventListener("change", () => {
        jar.checked = checkbox.checked;
        updateSelectionState();
      });

      const select = create("button", "jar-select");
      select.type = "button";
      select.title = jar.path;
      select.setAttribute(
        "aria-pressed",
        String(jar.path === state.activeJarPath),
      );
      const name = create("span", "jar-name", jar.name);
      const metadata = create(
        "span",
        "jar-meta",
        `${formatSize(jar.size)}  ·  ${jar.path}`,
      );
      select.append(name, metadata);
      select.addEventListener("click", () => selectJar(jar.path));

      const analyze = create("button", "icon-button compact jar-analyze");
      analyze.type = "button";
      analyze.title = `分析 ${jar.name}`;
      analyze.setAttribute("aria-label", `分析 ${jar.name}`);
      analyze.appendChild(createIcon("chart-no-axes-column-increasing"));
      analyze.addEventListener("click", () => selectJar(jar.path, true));

      item.append(checkbox, select, analyze);
      els.jarList.appendChild(item);
    });

    updateSelectionState();
    refreshIcons();
  }

  function setJarList(jars) {
    state.jars = (Array.isArray(jars) ? jars : []).map((jar) => ({
      name: String(jar.name || fileNameFromPath(jar.path)),
      path: String(jar.path || ""),
      size: Number(jar.size) || 0,
      checked: true,
    }));
    state.activeJarPath = state.jars[0] ? state.jars[0].path : null;
    renderJarList();
    renderAnalysisEmpty();
    if (state.jars[0]) loadJarAnalysis(state.jars[0]);
  }

  function selectJar(path, force = false) {
    const jar = state.jars.find((item) => item.path === path);
    if (!jar) return;
    state.activeJarPath = path;
    renderJarList();
    loadJarAnalysis(jar, force);
  }

  function renderAnalysisEmpty() {
    els.analysisEmpty.hidden = false;
    els.analysisContent.hidden = true;
    els.btnRefreshAnalysis.disabled = true;
    els.analysisState.textContent = "";
  }

  function renderAnalysisLoading(jar) {
    els.analysisEmpty.hidden = true;
    els.analysisContent.hidden = false;
    els.btnRefreshAnalysis.disabled = true;
    els.analysisName.textContent = jar.name;
    els.analysisPath.textContent = jar.path;
    els.analysisState.textContent = "正在分析...";
    els.analysisState.classList.remove("is-error");
    els.analysisTotal.textContent = "-";
    els.analysisClasses.textContent = "-";
    els.analysisResources.textContent = "-";
    els.analysisPackageCount.textContent = "-";
    els.analysisPackages.replaceChildren();
    els.analysisManifest.textContent = "";
  }

  function renderAnalysis(jar, analysis) {
    if (state.activeJarPath !== jar.path) return;
    els.analysisEmpty.hidden = true;
    els.analysisContent.hidden = false;
    els.btnRefreshAnalysis.disabled = false;
    els.analysisName.textContent = analysis.name || jar.name;
    els.analysisPath.textContent = analysis.path || jar.path;
    els.analysisState.textContent = `${formatSize(analysis.size || jar.size)}`;
    els.analysisState.classList.remove("is-error");
    els.analysisTotal.textContent = String(analysis.totalFiles ?? 0);
    els.analysisClasses.textContent = String(analysis.classFiles ?? 0);
    els.analysisResources.textContent = String(analysis.resourceFiles ?? 0);
    const allPackages = Array.isArray(analysis.packages)
      ? analysis.packages
      : [];
    const packages = allPackages.slice(0, MAX_PACKAGE_ROWS);
    const packageCount = Number.isFinite(Number(analysis.packageCount))
      ? Number(analysis.packageCount)
      : packages.length;
    els.analysisPackageCount.textContent = String(packageCount);
    els.analysisPackages.replaceChildren();
    if (packages.length === 0) {
      els.analysisPackages.appendChild(create("li", "", "未发现包信息"));
    } else {
      packages.forEach((packageName) => {
        els.analysisPackages.appendChild(create("li", "", packageName));
      });
      if (
        analysis.packagesTruncated ||
        allPackages.length > packages.length ||
        packageCount > packages.length
      ) {
        els.analysisPackages.appendChild(
          create(
            "li",
            "package-list-summary",
            `仅显示前 ${packages.length} 个，共 ${packageCount} 个`,
          ),
        );
      }
    }
    els.analysisManifest.textContent =
      analysis.manifest || "未包含 MANIFEST.MF";
  }

  async function loadJarAnalysis(jar, force = false) {
    if (!jar) {
      renderAnalysisEmpty();
      return;
    }
    if (!force && state.analysisCache.has(jar.path)) {
      renderAnalysis(jar, state.analysisCache.get(jar.path));
      return;
    }

    if (state.analysisController) state.analysisController.abort();
    const controller = new AbortController();
    state.analysisController = controller;
    renderAnalysisLoading(jar);

    try {
      const analysis = await api(
        "POST",
        "/api/analyze",
        { path: jar.path },
        { signal: controller.signal },
      );
      state.analysisCache.set(jar.path, analysis);
      renderAnalysis(jar, analysis);
    } catch (error) {
      if (isAbortError(error) || state.activeJarPath !== jar.path) return;
      els.analysisState.textContent = `分析失败: ${error.message}`;
      els.analysisState.classList.add("is-error");
      els.btnRefreshAnalysis.disabled = false;
    } finally {
      if (state.analysisController === controller)
        state.analysisController = null;
    }
  }

  function resetResultView() {
    if (state.searchController) state.searchController.abort();
    state.searchController = null;
    if (state.treeController) state.treeController.abort();
    state.treeController = null;
    state.outputDir = null;
    state.resultSessionId += 1;
    state.tabs = [];
    state.activeTab = null;
    state.pendingFiles.clear();
    els.tabBar.replaceChildren();
    els.fileTree.replaceChildren();
    els.treeEmpty.hidden = false;
    els.searchResults.replaceChildren();
    els.searchState.textContent = "";
    showEditorEmpty();
  }

  function releaseUploadDir(directory) {
    if (!directory) return;
    api("DELETE", `/api/upload?dir=${encodeURIComponent(directory)}`).catch(
      () => {},
    );
  }

  function invalidateScanSession(message, options = {}) {
    const hadState = Boolean(
      state.scanSession ||
        state.jars.length ||
        state.outputDir ||
        state.tabs.length,
    );
    const uploadDir =
      options.releaseUpload === false ? null : state.scanSession?.uploadDir;
    if (state.analysisController) state.analysisController.abort();
    state.analysisController = null;
    state.scanSession = null;
    state.jars = [];
    state.activeJarPath = null;
    state.analysisCache.clear();
    renderJarList();
    renderAnalysisEmpty();
    resetResultView();
    releaseUploadDir(uploadDir);
    if (hadState && message) setStatus(message);
  }

  async function scanDir() {
    const directory = els.sourceDir.value.trim();
    if (!directory) {
      toast("请输入源目录", "error");
      els.sourceDir.focus();
      return;
    }
    if (state.activeTask) return;

    const retainedUploadDir =
      state.scanSession?.uploadDir === directory
        ? state.scanSession.uploadDir
        : null;
    invalidateScanSession(undefined, { releaseUpload: !retainedUploadDir });
    const controller = startTask("正在扫描", { detail: directory });
    if (!controller) return;

    try {
      const data = await api(
        "POST",
        "/api/scan",
        { dir: directory },
        { signal: controller.signal },
      );
      const jars = Array.isArray(data.jars) ? data.jars : [];
      state.scanSession = {
        id: state.resultSessionId,
        inputDir: directory,
        sourceDir: data.dir || directory,
        uploadDir: retainedUploadDir,
      };
      setJarList(jars);
      if (jars.length === 0) {
        settleTask(controller, "success", "扫描完成", "未发现 JAR 文件", 100);
        setStatus("扫描完成，未发现 JAR", directory);
      } else {
        settleTask(
          controller,
          "success",
          "扫描完成",
          `发现 ${jars.length} 个 JAR 文件`,
          100,
        );
        setStatus(`已发现 ${jars.length} 个 JAR`, directory);
        toast(`已加入 ${jars.length} 个 JAR`, "success");
      }
    } catch (error) {
      if (isAbortError(error)) {
        settleTask(controller, "idle", "扫描已取消", directory);
        setStatus("扫描已取消");
        return;
      }
      showTaskError(controller, "扫描失败", error);
    }
  }

  function setMode(mode) {
    if (!["decompile", "extract"].includes(mode) || state.activeTask) return;
    state.mode = mode;
    const decompile = mode === "decompile";
    els.modeDecompile.setAttribute("aria-pressed", String(decompile));
    els.modeExtract.setAttribute("aria-pressed", String(!decompile));
    els.filterField.hidden = !decompile;
    const label = $("span", els.btnRun);
    if (label) label.textContent = decompile ? "开始反编译" : "开始解包";
  }

  async function runSelectedTask() {
    if (state.activeTask) return;
    const selected = selectedJars();
    if (selected.length === 0) {
      toast("请至少选择一个 JAR", "error");
      return;
    }
    if (!state.scanSession) {
      toast("源目录已改变，请重新扫描", "error");
      return;
    }

    let outputDir = els.outputDir.value.trim();
    if (!outputDir) {
      outputDir =
        state.scanSession.defaultOutputDir ||
        buildDefaultOutputDir(state.scanSession.sourceDir, state.mode);
      els.outputDir.value = outputDir;
    }

    resetResultView();
    saveTaskLog("");
    const isDecompile = state.mode === "decompile";
    const label = isDecompile ? "正在反编译" : "正在解包";
    const controller = startTask(label, {
      detail: `${selected.length} 个 JAR`,
      percent: isDecompile ? 0 : undefined,
    });
    if (!controller) return;

    try {
      let result;
      if (isDecompile) {
        result = await streamDecompileProgress(
          {
            jars: selected.map((jar) => jar.path),
            outputDir,
            filterPkg: els.filterPackage.value.trim(),
          },
          controller,
        );
      } else {
        result = await api(
          "POST",
          "/api/extract",
          {
            jars: selected.map((jar) => ({
              name: jar.name,
              path: jar.path,
              size: jar.size,
            })),
            outputDir,
          },
          { signal: controller.signal },
        );
      }

      state.outputDir = result.outputDir || outputDir;
      els.outputDir.value = state.outputDir;
      updateTask(controller, {
        title: "正在加载结果",
        detail: state.outputDir,
        percent: 100,
      });
      await loadFileTree(state.outputDir, controller.signal);

      const resultDetail = isDecompile
        ? `${result.javaFiles || 0} 个 Java 文件${result.elapsed ? ` · ${result.elapsed}` : ""}`
        : `${result.totalFiles || 0} 个文件 · ${result.jarCount || selected.length} 个 JAR`;
      settleTask(
        controller,
        "success",
        isDecompile ? "反编译完成" : "解包完成",
        resultDetail,
        100,
      );
      setStatus(isDecompile ? "反编译完成" : "解包完成", state.outputDir);
      toast(isDecompile ? "反编译完成" : "解包完成", "success");
    } catch (error) {
      if (isAbortError(error)) {
        settleTask(
          controller,
          "idle",
          `${isDecompile ? "反编译" : "解包"}已取消`,
          "未保留为当前结果",
        );
        setStatus("任务已取消");
        return;
      }
      showTaskError(
        controller,
        `${isDecompile ? "反编译" : "解包"}失败`,
        error,
      );
    }
  }

  async function streamDecompileProgress(payload, controller) {
    const response = await apiFetch("/api/decompile/stream", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: controller.signal,
    });

    if (!response.ok) {
      const data = await readResponseData(response);
      throw taskError(apiErrorMessage(response, data), data.log || "");
    }
    if (!response.body) throw new Error("当前浏览器不支持进度流");

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let finalResult = null;

    const consumeLine = (line) => {
      if (!line.trim()) return;
      const event = JSON.parse(line);
      if (event.type === "error") {
        throw taskError(event.message || "反编译失败", event.log || "");
      }
      if (event.log) saveTaskLog(event.log);
      if (event.type === "done") finalResult = event.result || null;

      const details = [event.detail, event.jar].filter(Boolean).join(" · ");
      const indeterminate =
        event.type === "heartbeat" || !Number.isFinite(event.percent);
      updateTask(controller, {
        title: event.message || "正在反编译",
        detail: details || (event.type === "start" ? "准备反编译器" : ""),
        percent: indeterminate ? undefined : event.percent,
        indeterminate,
      });
      if (event.message) setStatus(event.message, details);
    };

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";
      lines.forEach(consumeLine);
    }

    buffer += decoder.decode();
    if (buffer.trim()) consumeLine(buffer);
    if (!finalResult) throw new Error("反编译服务未返回完成结果");
    return finalResult;
  }

  async function loadFileTree(rootDir, signal) {
    if (state.treeController) state.treeController.abort();
    const controller = new AbortController();
    state.treeController = controller;
    if (signal) {
      if (signal.aborted) controller.abort();
      else
        signal.addEventListener("abort", () => controller.abort(), {
          once: true,
        });
    }

    const resultSessionId = state.resultSessionId;
    const tree = await fetchTreePage(rootDir, "", 0, controller.signal);
    if (
      resultSessionId !== state.resultSessionId ||
      rootDir !== state.outputDir ||
      controller.signal.aborted
    )
      return;

    els.fileTree.replaceChildren();
    const nodes = Array.isArray(tree.children) ? tree.children : [];
    els.treeEmpty.hidden = nodes.length > 0;
    if (nodes.length === 0) return;

    const rootList = create("ul", "tree-list");
    rootList.setAttribute("role", "none");
    appendTreePage(rootList, tree, rootDir, "", 1, resultSessionId, controller);
    els.fileTree.appendChild(rootList);
    const first = $("[role='treeitem']", els.fileTree);
    if (first) first.tabIndex = 0;
    refreshIcons();
  }

  function fetchTreePage(
    rootDir,
    relativePath,
    offset,
    signal,
    limit = TREE_PAGE_SIZE,
  ) {
    const params = new URLSearchParams({
      root: rootDir,
      path: relativePath || "",
      offset: String(offset),
      limit: String(limit),
    });
    return api("GET", `/api/tree?${params}`, null, { signal });
  }

  function appendTreePage(
    container,
    page,
    rootDir,
    relativePath,
    level,
    resultSessionId,
    treeController,
  ) {
    const existingLoadMore = $(":scope > .tree-load-more-item", container);
    if (existingLoadMore) existingLoadMore.remove();

    const nodes = Array.isArray(page.children) ? page.children : [];
    const fragment = document.createDocumentFragment();
    renderTreeNodes(
      fragment,
      nodes,
      rootDir,
      level,
      resultSessionId,
      treeController,
    );
    const firstNewRow = $("[role='treeitem']", fragment);
    container.appendChild(fragment);

    const nextOffset = Math.max(0, Number(page.offset) || 0) + nodes.length;
    let loadMoreButton = null;
    if (page.hasMore) {
      const loadMoreItem = createTreeLoadMore(
        container,
        rootDir,
        relativePath,
        level,
        nextOffset,
        resultSessionId,
        treeController,
      );
      loadMoreButton = $("[role='treeitem']", loadMoreItem);
      container.appendChild(loadMoreItem);
    }
    refreshIcons();
    return { firstNewRow, loadMoreButton };
  }

  function createTreeLoadMore(
    container,
    rootDir,
    relativePath,
    level,
    offset,
    resultSessionId,
    treeController,
  ) {
    const listItem = create("li", "tree-load-more-item");
    listItem.setAttribute("role", "none");
    const button = create("button", "tree-load-more");
    button.type = "button";
    button.setAttribute("role", "treeitem");
    button.setAttribute("aria-level", String(level));
    button.tabIndex = -1;
    button.style.paddingLeft = `${Math.max(0, level - 1) * 14 + 46}px`;
    button.append(createIcon("list-plus"), create("span", "", "继续加载"));
    button.addEventListener("click", async () => {
      if (
        button.disabled ||
        state.treeController !== treeController ||
        treeController.signal.aborted
      )
        return;
      const capacity = remainingTreeCapacity();
      if (capacity <= 0) {
        toast(`文件树最多同时显示 ${MAX_TREE_ROWS} 项，请先折叠其他目录`);
        return;
      }
      const restoreFocus = document.activeElement === button;
      button.disabled = true;
      button.classList.add("is-loading");
      try {
        const page = await fetchTreePage(
          rootDir,
          relativePath,
          offset,
          treeController.signal,
          Math.min(TREE_PAGE_SIZE, capacity),
        );
        if (
          resultSessionId !== state.resultSessionId ||
          rootDir !== state.outputDir ||
          state.treeController !== treeController ||
          !button.isConnected
        )
          return;
        const nodes = Array.isArray(page.children) ? page.children : [];
        if (nodes.length > remainingTreeCapacity()) {
          toast(`文件树最多同时显示 ${MAX_TREE_ROWS} 项，请先折叠其他目录`);
          button.disabled = false;
          button.classList.remove("is-loading");
          return;
        }
        const appended = appendTreePage(
          container,
          page,
          rootDir,
          relativePath,
          level,
          resultSessionId,
          treeController,
        );
        if (restoreFocus)
          focusTreeRow(appended.firstNewRow || appended.loadMoreButton);
      } catch (error) {
        if (!isAbortError(error)) {
          toast(`加载目录失败: ${error.message}`, "error");
          button.disabled = false;
          button.classList.remove("is-loading");
        }
      }
    });
    listItem.appendChild(button);
    return listItem;
  }

  function renderTreeNodes(
    container,
    nodes,
    rootDir,
    level,
    resultSessionId,
    treeController,
  ) {
    nodes.forEach((node) => {
      const listItem = create("li");
      listItem.setAttribute("role", "none");

      const row = create("button", "tree-row");
      row.type = "button";
      row.setAttribute("role", "treeitem");
      row.setAttribute("aria-level", String(level));
      row.tabIndex = -1;
      const fullPath = joinServerPath(rootDir, node.path);
      row.dataset.path = fullPath;
      row.dataset.relativePath = String(node.path || "");
      row.title = fullPath;

      const indent = create("span", "tree-indent");
      indent.style.width = `${Math.max(0, level - 1) * 14}px`;
      const chevron = create("span", "tree-chevron");
      const nodeIcon = create("span", "tree-icon");
      const name = create("span", "tree-name", node.name);

      if (node.type === "dir") {
        nodeIcon.appendChild(createIcon("folder"));
        if (node.hasChildren) {
          row.setAttribute("aria-expanded", "false");
          chevron.appendChild(createIcon("chevron-right"));
          const group = create("ul", "tree-group");
          group.setAttribute("role", "group");
          group.hidden = true;
          row.treeToggle = (force) =>
            toggleTreeDirectory(
              row,
              group,
              rootDir,
              String(node.path || ""),
              level + 1,
              resultSessionId,
              treeController,
              force,
            );
          row.addEventListener("click", () => row.treeToggle());
          listItem.append(row, group);
        } else {
          chevron.setAttribute("aria-hidden", "true");
          listItem.appendChild(row);
        }
      } else {
        chevron.setAttribute("aria-hidden", "true");
        nodeIcon.appendChild(createIcon(fileIconName(node.name)));
        row.addEventListener("click", () => openFile(fullPath, node.name));
        const activeTab = state.tabs.find((tab) => tab.id === state.activeTab);
        if (activeTab && activeTab.path === fullPath) {
          row.classList.add("is-active");
          row.setAttribute("aria-current", "true");
        }
        listItem.appendChild(row);
      }

      row.append(indent, chevron, nodeIcon, name);
      container.appendChild(listItem);
    });
  }

  async function toggleTreeDirectory(
    row,
    group,
    rootDir,
    relativePath,
    childLevel,
    resultSessionId,
    treeController,
    force,
  ) {
    const expanded =
      force === undefined
        ? row.getAttribute("aria-expanded") !== "true"
        : force;
    toggleTreeRow(row, group, expanded);
    if (!expanded) {
      if (group.dataset.loaded === "true") {
        group.replaceChildren();
        delete group.dataset.loaded;
      }
      return;
    }
    if (group.dataset.loaded === "true" || row.dataset.loading) return;

    const capacity = remainingTreeCapacity();
    if (capacity <= 0) {
      toggleTreeRow(row, group, false);
      toast(`文件树最多同时显示 ${MAX_TREE_ROWS} 项，请先折叠其他目录`);
      return;
    }

    row.dataset.loading = "true";
    row.classList.add("is-loading");
    try {
      if (
        state.treeController !== treeController ||
        treeController.signal.aborted
      )
        return;
      const page = await fetchTreePage(
        rootDir,
        relativePath,
        0,
        treeController.signal,
        Math.min(TREE_PAGE_SIZE, capacity),
      );
      if (
        resultSessionId !== state.resultSessionId ||
        rootDir !== state.outputDir ||
        state.treeController !== treeController ||
        row.getAttribute("aria-expanded") !== "true" ||
        !row.isConnected
      )
        return;
      const nodes = Array.isArray(page.children) ? page.children : [];
      if (nodes.length > remainingTreeCapacity()) {
        toggleTreeRow(row, group, false);
        toast(`文件树最多同时显示 ${MAX_TREE_ROWS} 项，请先折叠其他目录`);
        return;
      }
      group.replaceChildren();
      appendTreePage(
        group,
        page,
        rootDir,
        relativePath,
        childLevel,
        resultSessionId,
        treeController,
      );
      group.dataset.loaded = "true";
    } catch (error) {
      if (!isAbortError(error)) {
        toggleTreeRow(row, group, false);
        toast(`加载目录失败: ${error.message}`, "error");
      }
    } finally {
      delete row.dataset.loading;
      row.classList.remove("is-loading");
    }
  }

  function remainingTreeCapacity() {
    return Math.max(0, MAX_TREE_ROWS - $$(".tree-row", els.fileTree).length);
  }

  function toggleTreeRow(row, group, force) {
    const expanded =
      force === undefined
        ? row.getAttribute("aria-expanded") !== "true"
        : force;
    row.setAttribute("aria-expanded", String(expanded));
    group.hidden = !expanded;
  }

  function visibleTreeRows() {
    return $$("[role='treeitem']", els.fileTree).filter(
      (row) => !row.closest("[role='group'][hidden]"),
    );
  }

  function focusTreeRow(row) {
    if (!row) return;
    $$("[role='treeitem']", els.fileTree).forEach((item) => {
      item.tabIndex = item === row ? 0 : -1;
    });
    row.focus();
  }

  function handleTreeKeydown(event) {
    const row = event.target.closest("[role='treeitem']");
    if (!row) return;
    const rows = visibleTreeRows();
    const index = rows.indexOf(row);

    if (event.key === "ArrowDown") {
      event.preventDefault();
      focusTreeRow(rows[Math.min(rows.length - 1, index + 1)]);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      focusTreeRow(rows[Math.max(0, index - 1)]);
    } else if (event.key === "Home") {
      event.preventDefault();
      focusTreeRow(rows[0]);
    } else if (event.key === "End") {
      event.preventDefault();
      focusTreeRow(rows[rows.length - 1]);
    } else if (
      event.key === "ArrowRight" &&
      row.hasAttribute("aria-expanded")
    ) {
      event.preventDefault();
      const group = row.nextElementSibling;
      if (row.getAttribute("aria-expanded") !== "true") {
        if (typeof row.treeToggle === "function") row.treeToggle(true);
        else toggleTreeRow(row, group, true);
      } else focusTreeRow($("[role='treeitem']", group));
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      if (row.getAttribute("aria-expanded") === "true") {
        if (typeof row.treeToggle === "function") row.treeToggle(false);
        else toggleTreeRow(row, row.nextElementSibling, false);
      } else {
        const parentGroup =
          row.parentElement && row.parentElement.parentElement;
        const parentItem =
          parentGroup && parentGroup.closest("li[role='none']");
        const parentRow = parentItem
          ? $(":scope > [role='treeitem']", parentItem)
          : null;
        focusTreeRow(parentRow || row);
      }
    }
  }

  async function openFile(path, name, locationHint = {}) {
    const existing = state.tabs.find((tab) => tab.path === path);
    if (existing) {
      activateTab(existing.id, locationHint);
      return;
    }

    if (state.pendingFiles.has(path)) {
      await state.pendingFiles.get(path);
      const pendingTab = state.tabs.find((tab) => tab.path === path);
      if (pendingTab) activateTab(pendingTab.id, locationHint);
      return;
    }

    const resultSessionId = state.resultSessionId;
    const request = (async () => {
      try {
        setStatus(`正在打开 ${name}`, path);
        const data = await api(
          "GET",
          `/api/file?path=${encodeURIComponent(path)}`,
        );
        if (resultSessionId !== state.resultSessionId) return;
        const duplicate = state.tabs.find((tab) => tab.path === path);
        if (duplicate) {
          activateTab(duplicate.id, locationHint);
          return;
        }
        const tab = {
          id: `source-tab-${++state.tabSequence}`,
          path,
          name: String(data.name || name || fileNameFromPath(path)),
          content: String(data.content || ""),
          size: Number(data.size) || 0,
          targetLine: Number(locationHint.line) || null,
        };
        state.tabs.push(tab);
        activateTab(tab.id, locationHint);
      } catch (error) {
        toast(`打开文件失败: ${error.message}`, "error");
        setStatus("打开文件失败", path);
      }
    })();

    state.pendingFiles.set(path, request);
    try {
      await request;
    } finally {
      state.pendingFiles.delete(path);
    }
  }

  function uniqueTabLabel(tab) {
    const duplicates = state.tabs.filter((item) => item.name === tab.name);
    if (duplicates.length < 2) return tab.name;
    const parts = tab.path.split(/[\\/]/);
    return parts.slice(-2).join("/");
  }

  function renderTabBar() {
    els.tabBar.replaceChildren();
    state.tabs.forEach((tab) => {
      const item = create("div", "tab-item");
      const button = create("button", "tab-button");
      button.type = "button";
      button.id = tab.id;
      button.setAttribute("role", "tab");
      button.setAttribute("aria-selected", String(tab.id === state.activeTab));
      button.setAttribute("aria-controls", "code-viewer");
      button.tabIndex = tab.id === state.activeTab ? 0 : -1;
      button.title = tab.path;
      button.append(
        createIcon(fileIconName(tab.name)),
        create("span", "tab-label", uniqueTabLabel(tab)),
      );
      button.addEventListener("click", () => activateTab(tab.id));

      const close = create("button", "tab-close");
      close.type = "button";
      close.setAttribute("aria-label", `关闭 ${tab.name}`);
      close.title = `关闭 ${tab.name}`;
      close.appendChild(createIcon("x"));
      close.addEventListener("click", () => closeTab(tab.id));

      item.append(button, close);
      els.tabBar.appendChild(item);
    });
    refreshIcons();
  }

  function activateTab(tabId, locationHint = {}) {
    const tab = state.tabs.find((item) => item.id === tabId);
    if (!tab) return;
    state.activeTab = tabId;
    if (Number(locationHint.line) > 0)
      tab.targetLine = Number(locationHint.line);
    renderTabBar();
    renderCode(tab.content, tab.name, tab.targetLine);
    setStatus(
      tab.name,
      `${tab.path} · ${formatSize(tab.size)}${tab.targetLine ? ` · 第 ${tab.targetLine} 行` : ""}`,
    );
    $$("[role='treeitem']", els.fileTree).forEach((row) => {
      const active = row.dataset.path === tab.path;
      row.classList.toggle("is-active", active);
      if (active) row.setAttribute("aria-current", "true");
      else row.removeAttribute("aria-current");
    });
  }

  function closeTab(tabId) {
    const index = state.tabs.findIndex((tab) => tab.id === tabId);
    if (index < 0) return;
    const wasActive = state.activeTab === tabId;
    state.tabs.splice(index, 1);
    if (wasActive) {
      const next = state.tabs[Math.min(index, state.tabs.length - 1)];
      state.activeTab = next ? next.id : null;
      if (next) activateTab(next.id);
      else {
        renderTabBar();
        showEditorEmpty();
        setStatus("结果已加载", state.outputDir || "");
      }
    } else {
      renderTabBar();
    }
  }

  function handleTabKeydown(event) {
    const tabButton = event.target.closest("[role='tab']");
    if (!tabButton) return;
    const tabs = $$('[role="tab"]', els.tabBar);
    const index = tabs.indexOf(tabButton);
    let target = null;
    if (event.key === "ArrowRight") target = tabs[(index + 1) % tabs.length];
    else if (event.key === "ArrowLeft")
      target = tabs[(index - 1 + tabs.length) % tabs.length];
    else if (event.key === "Home") target = tabs[0];
    else if (event.key === "End") target = tabs[tabs.length - 1];
    else if (event.key === "Delete" || event.key === "Backspace") {
      event.preventDefault();
      closeTab(tabButton.id);
      return;
    }
    if (target) {
      event.preventDefault();
      activateTab(target.id);
      target.focus();
    }
  }

  function renderCode(content, filename, targetLine) {
    const lineCount = countLinesUpTo(content, MAX_NUMBERED_LINES + 1);
    const hasBoundedLines = lineCount <= MAX_NUMBERED_LINES;
    const isLargePreview =
      content.length > MAX_HIGHLIGHT_CHARS || !hasBoundedLines;
    if (isLargePreview) {
      renderLargeCode(content, filename, targetLine);
      return;
    }

    const container = create("div", "code-container");
    const lineNumbers = create("pre", "line-numbers");
    lineNumbers.textContent = Array.from(
      { length: lineCount },
      (_, index) => index + 1,
    ).join("\n");

    const codeContent = create("div", "code-content");
    const pre = create("pre");
    const code = create("code", "hljs");
    const extension = String(filename || "")
      .split(".")
      .pop()
      .toLowerCase();
    const language =
      { java: "java", xml: "xml", properties: "properties", json: "json" }[
        extension
      ] || "plaintext";

    if (window.hljs && typeof window.hljs.highlight === "function") {
      try {
        code.innerHTML = window.hljs.highlight(content, {
          language,
          ignoreIllegals: true,
        }).value;
      } catch {
        code.textContent = content;
      }
    } else {
      code.textContent = content;
    }

    pre.appendChild(code);
    if (Number(targetLine) > 0) {
      const line = Math.min(lineCount, Math.max(1, Number(targetLine)));
      const indicator = create("div", "current-line");
      indicator.style.top = `${CODE_TOP_PADDING + (line - 1) * CODE_LINE_HEIGHT}px`;
      indicator.setAttribute("aria-label", `第 ${line} 行`);
      codeContent.appendChild(indicator);
    }
    codeContent.appendChild(pre);
    container.append(lineNumbers, codeContent);
    els.codeViewer.replaceChildren(container);

    if (Number(targetLine) > 0) {
      window.requestAnimationFrame(() => {
        els.codeViewer.scrollTop = Math.max(
          0,
          (Number(targetLine) - 1) * CODE_LINE_HEIGHT - 84,
        );
      });
    }
  }

  function renderLargeCode(content, filename, targetLine) {
    const preview = create("textarea", "large-code-preview");
    preview.readOnly = true;
    preview.spellcheck = false;
    preview.wrap = "off";
    preview.value = content;
    preview.setAttribute("aria-label", `${filename || "源码"} 纯文本预览`);
    els.codeViewer.replaceChildren(preview);

    if (Number(targetLine) > 0) {
      window.requestAnimationFrame(() => {
        preview.scrollTop = Math.max(
          0,
          (Number(targetLine) - 1) * CODE_LINE_HEIGHT - 84,
        );
      });
    }
  }

  function countLinesUpTo(content, limit) {
    let lines = 1;
    for (let index = 0; index < content.length && lines < limit; index += 1) {
      if (content.charCodeAt(index) === 10) lines += 1;
    }
    return lines;
  }

  function showEditorEmpty() {
    const empty = create("div", "editor-empty");
    const mark = create("div", "editor-empty-mark");
    mark.appendChild(createIcon("file-code-2"));
    empty.append(
      mark,
      create("h1", "", "源码工作区"),
      create("p", "", "选择任务结果中的文件"),
    );
    els.codeViewer.replaceChildren(empty);
    refreshIcons();
  }

  function activateResultTab(name, focus = false) {
    const search = name === "search";
    els.resultFilesTab.setAttribute("aria-selected", String(!search));
    els.resultFilesTab.tabIndex = search ? -1 : 0;
    els.resultSearchTab.setAttribute("aria-selected", String(search));
    els.resultSearchTab.tabIndex = search ? 0 : -1;
    els.filesPanel.hidden = search;
    els.searchPanel.hidden = !search;
    if (focus) {
      if (search) els.searchKeyword.focus();
      else els.resultFilesTab.focus();
    }
  }

  function handleResultTabKeydown(event) {
    if (!event.target.matches("[role='tab']")) return;
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const search = event.target === els.resultFilesTab;
    activateResultTab(search ? "search" : "files", true);
  }

  async function doSearch() {
    const keyword = els.searchKeyword.value.trim();
    if (!state.outputDir) {
      toast("请先完成任务", "error");
      return;
    }
    if (!keyword) {
      els.searchKeyword.focus();
      return;
    }

    if (state.searchController) state.searchController.abort();
    const controller = new AbortController();
    const resultRoot = state.outputDir;
    const resultSessionId = state.resultSessionId;
    state.searchController = controller;
    els.searchState.textContent = "正在搜索...";
    els.searchState.classList.remove("is-error");
    els.searchResults.replaceChildren();
    els.btnSearch.disabled = true;

    try {
      const data = await api(
        "POST",
        "/api/search",
        {
          dir: resultRoot,
          keyword,
          ignoreCase: els.searchIgnoreCase.checked,
          maxResults: 100,
        },
        { signal: controller.signal },
      );
      if (resultSessionId !== state.resultSessionId) return;
      const results = Array.isArray(data.results) ? data.results : [];
      els.searchState.textContent =
        results.length >= 100
          ? "显示前 100 处匹配"
          : `${results.length} 处匹配`;
      renderSearchResults(results, keyword, resultRoot);
    } catch (error) {
      if (isAbortError(error)) return;
      els.searchState.textContent = `搜索失败: ${error.message}`;
      els.searchState.classList.add("is-error");
    } finally {
      if (state.searchController === controller) state.searchController = null;
      els.btnSearch.disabled = false;
    }
  }

  function renderSearchResults(results, keyword, resultRoot) {
    els.searchResults.replaceChildren();
    if (results.length === 0) return;
    results.forEach((result) => {
      const listItem = create("li");
      const item = create("button", "search-result");
      item.type = "button";
      const file = create("span", "search-result-file", result.file);
      const snippet = create("span", "search-result-snippet");
      appendHighlightedText(
        snippet,
        String(result.content || "").trim(),
        keyword,
        els.searchIgnoreCase.checked,
      );
      const metadata = create(
        "span",
        "search-result-meta",
        `第 ${result.line} 行`,
      );
      item.append(file, snippet, metadata);
      item.addEventListener("click", () => {
        const fullPath = joinServerPath(resultRoot, result.file);
        openFile(fullPath, fileNameFromPath(result.file), {
          line: result.line,
        });
      });
      listItem.appendChild(item);
      els.searchResults.appendChild(listItem);
    });
  }

  function appendHighlightedText(container, text, keyword, ignoreCase) {
    if (!keyword) {
      container.textContent = text;
      return;
    }
    const source = ignoreCase ? text.toLocaleLowerCase() : text;
    const target = ignoreCase ? keyword.toLocaleLowerCase() : keyword;
    if (!target) {
      container.textContent = text;
      return;
    }
    let cursor = 0;
    let marked = 0;
    while (cursor < text.length && marked < MAX_SEARCH_MARKS) {
      const index = source.indexOf(target, cursor);
      if (index < 0) {
        container.appendChild(document.createTextNode(text.slice(cursor)));
        cursor = text.length;
        break;
      }
      if (index > cursor)
        container.appendChild(
          document.createTextNode(text.slice(cursor, index)),
        );
      container.appendChild(
        create("mark", "", text.slice(index, index + keyword.length)),
      );
      cursor = index + keyword.length;
      marked += 1;
    }
    if (cursor < text.length)
      container.appendChild(document.createTextNode(text.slice(cursor)));
  }

  async function openBrowse(target) {
    state.browseTarget = target;
    els.browseTitle.textContent =
      target === "source" ? "选择源目录" : "选择输出目录";
    openDialog(els.browseDialog, "#browse-current-dir");
    const initial =
      target === "source"
        ? els.sourceDir.value.trim()
        : els.outputDir.value.trim();
    await browseTo(initial);
  }

  async function browseTo(directory) {
    els.browseState.textContent = "正在读取...";
    els.browseState.classList.remove("is-error");
    els.browseList.replaceChildren();
    try {
      const url = directory
        ? `/api/browse?dir=${encodeURIComponent(directory)}`
        : "/api/browse";
      const data = await api("GET", url);
      state.browseParent = data.parent || null;
      els.browseCurrentDir.value = data.dir || "";
      els.browseUp.disabled =
        !state.browseParent || state.browseParent === data.dir;
      els.browseState.textContent = `${Array.isArray(data.entries) ? data.entries.length : 0} 个项目`;
      renderBrowseEntries(Array.isArray(data.entries) ? data.entries : []);
    } catch (error) {
      els.browseState.textContent = `无法读取: ${error.message}`;
      els.browseState.classList.add("is-error");
    }
  }

  function renderBrowseEntries(entries) {
    els.browseList.replaceChildren();
    entries.forEach((entry) => {
      const listItem = create("li");
      const button = create("button", "browse-entry");
      button.type = "button";
      button.title = entry.path;
      button.appendChild(
        createIcon(
          entry.type === "dir" ? "folder" : entry.isJar ? "package" : "file",
        ),
      );
      button.appendChild(create("span", "browse-entry-name", entry.name));
      button.appendChild(
        create(
          "span",
          "browse-entry-size",
          entry.type === "dir" ? "" : formatSize(entry.size),
        ),
      );
      if (entry.type === "dir") {
        button.addEventListener("click", () => browseTo(entry.path));
      } else {
        button.disabled = true;
        button.setAttribute("aria-disabled", "true");
      }
      listItem.appendChild(button);
      els.browseList.appendChild(listItem);
    });
    refreshIcons();
  }

  function selectBrowsedDirectory() {
    const directory = els.browseCurrentDir.value.trim();
    if (!directory) return;
    if (state.browseTarget === "source") {
      const changed = els.sourceDir.value.trim() !== directory;
      els.sourceDir.value = directory;
      if (changed) invalidateScanSession("源目录已更改，请重新扫描");
    } else {
      els.outputDir.value = directory;
    }
    closeDialog(els.browseDialog);
  }

  async function loadSettings() {
    openDialog(els.settingsDialog, "#setting-java-path");
    els.settingsState.textContent = "正在读取设置...";
    els.settingsState.classList.remove("is-error");
    els.settingsSave.disabled = true;
    try {
      const config = await api("GET", "/api/config");
      els.settingJavaPath.value = config.javaPath || "";
      els.settingCfrPath.value = config.cfrPath || "";
      els.settingJavaPath.placeholder = config.effectiveJavaPath
        ? `自动: ${config.effectiveJavaPath}`
        : "留空自动检测";
      els.settingCfrPath.placeholder = config.effectiveCfrPath
        ? `自动: ${config.effectiveCfrPath}`
        : "留空自动识别";
      els.settingsState.textContent = config.decompilerVersion
        ? `${config.decompilerName || "Fernflower"} ${config.decompilerVersion}`
        : "";
    } catch (error) {
      els.settingsState.textContent = `读取失败: ${error.message}`;
      els.settingsState.classList.add("is-error");
    } finally {
      els.settingsSave.disabled = false;
    }
  }

  async function saveSettings() {
    els.settingsSave.disabled = true;
    els.settingsState.textContent = "正在保存...";
    els.settingsState.classList.remove("is-error");
    try {
      await api("PUT", "/api/config", {
        javaPath: els.settingJavaPath.value.trim(),
        cfrPath: els.settingCfrPath.value.trim(),
      });
      closeDialog(els.settingsDialog);
      toast("设置已保存", "success");
    } catch (error) {
      els.settingsState.textContent = `保存失败: ${error.message}`;
      els.settingsState.classList.add("is-error");
    } finally {
      els.settingsSave.disabled = false;
    }
  }

  let dragDepth = 0;

  function setupDragAndDrop() {
    document.addEventListener("dragenter", (event) => {
      event.preventDefault();
      dragDepth += 1;
      if (!state.activeTask) els.dragOverlay.hidden = false;
    });
    document.addEventListener("dragover", (event) => event.preventDefault());
    document.addEventListener("dragleave", (event) => {
      event.preventDefault();
      dragDepth -= 1;
      if (dragDepth <= 0) {
        dragDepth = 0;
        els.dragOverlay.hidden = true;
      }
    });
    document.addEventListener("drop", (event) => {
      event.preventDefault();
      dragDepth = 0;
      els.dragOverlay.hidden = true;
      if (!state.activeTask) handleDrop(event);
    });
  }

  async function handleDrop(event) {
    const items = event.dataTransfer.items;
    const files = event.dataTransfer.files;
    if ((!items || items.length === 0) && (!files || files.length === 0))
      return;

    invalidateScanSession();
    const controller = startTask("正在导入", { detail: "读取拖入内容" });
    if (!controller) return;
    const jarFiles = [];
    const seen = new Set();

    try {
      Array.from(files || []).forEach((file) =>
        addJarFile(file, jarFiles, seen),
      );
      for (let index = 0; index < (items ? items.length : 0); index += 1) {
        controller.signal.throwIfAborted();
        const entry = items[index].webkitGetAsEntry
          ? items[index].webkitGetAsEntry()
          : null;
        if (entry)
          await collectJarEntries(entry, jarFiles, seen, controller.signal);
        else addJarFile(files[index], jarFiles, seen);
      }

      if (jarFiles.length === 0) {
        settleTask(controller, "idle", "导入完成", "未发现 JAR 文件");
        return;
      }

      updateTask(controller, {
        title: "正在上传",
        detail: `${jarFiles.length} 个 JAR`,
      });
      const formData = new FormData();
      jarFiles.forEach((file) => formData.append("files", file));
      const response = await apiFetch("/api/upload", {
        method: "POST",
        body: formData,
        signal: controller.signal,
      });
      const data = await readResponseData(response);
      if (!response.ok)
        throw taskError(apiErrorMessage(response, data), data.log || "");

      const uploaded = Array.isArray(data.files) ? data.files : [];
      els.sourceDir.value = data.tempDir || "";
      if (data.suggestedOutputDir)
        els.outputDir.value = data.suggestedOutputDir;
      state.scanSession = {
        id: state.resultSessionId,
        inputDir: data.tempDir || "",
        sourceDir: data.tempDir || "",
        uploadDir: data.tempDir || "",
        defaultOutputDir: data.suggestedOutputDir || "",
      };
      setJarList(uploaded);
      settleTask(
        controller,
        "success",
        "导入完成",
        `已加入 ${uploaded.length} 个 JAR`,
        100,
      );
      setStatus(`已导入 ${uploaded.length} 个 JAR`, data.tempDir || "");
      if (window.matchMedia("(max-width: 760px)").matches) setSidebarOpen(true);
    } catch (error) {
      if (isAbortError(error)) {
        settleTask(controller, "idle", "导入已取消", "");
        setStatus("导入已取消");
        return;
      }
      showTaskError(controller, "导入失败", error);
    }
  }

  function addJarFile(file, results, seen) {
    if (!file || !file.name || !file.name.toLowerCase().endsWith(".jar"))
      return;
    const key = `${file.webkitRelativePath || file.name}:${file.size}:${file.lastModified}`;
    if (seen.has(key)) return;
    seen.add(key);
    results.push(file);
  }

  async function collectJarEntries(entry, results, seen, signal) {
    signal.throwIfAborted();
    if (entry.isFile) {
      if (!entry.name.toLowerCase().endsWith(".jar")) return;
      const file = await new Promise((resolve, reject) =>
        entry.file(resolve, reject),
      );
      addJarFile(file, results, seen);
      return;
    }
    if (!entry.isDirectory) return;
    const entries = await readAllEntries(entry.createReader());
    for (const child of entries) {
      await collectJarEntries(child, results, seen, signal);
    }
  }

  function readAllEntries(reader) {
    return new Promise((resolve, reject) => {
      const entries = [];
      const readBatch = () => {
        reader.readEntries((batch) => {
          if (batch.length === 0) resolve(entries);
          else {
            entries.push(...batch);
            readBatch();
          }
        }, reject);
      };
      readBatch();
    });
  }

  function isEditableTarget(target) {
    return (
      target instanceof HTMLElement &&
      (target.isContentEditable || target.matches("input, textarea, select"))
    );
  }

  function handleGlobalKeydown(event) {
    const modifier = event.ctrlKey || event.metaKey;
    const openDialogElement = $("dialog[open]");
    if (openDialogElement) return;

    if (event.key === "Escape" && state.activeTask) {
      event.preventDefault();
      cancelActiveTask();
      return;
    }
    if (isEditableTarget(event.target)) return;

    if (modifier && event.key.toLowerCase() === "f") {
      event.preventDefault();
      activateResultTab("search", true);
    } else if (modifier && event.key.toLowerCase() === "o") {
      event.preventDefault();
      openBrowse("source");
    } else if (modifier && event.key === "Enter") {
      event.preventDefault();
      if (state.activeTask) return;
      if (selectedJars().length > 0) runSelectedTask();
      else scanDir();
    }
  }

  const layoutPreferences = readLayoutPreferences();
  let layoutSyncFrame = 0;
  let layoutResizeObserver = null;

  function readLayoutPreferences() {
    try {
      const parsed = JSON.parse(
        localStorage.getItem(LAYOUT_STORAGE_KEY) || "{}",
      );
      const preferences = {};
      ["sidebarWidth", "queueHeight", "resultWidth", "resultHeight"].forEach(
        (key) => {
          const value = Number(parsed[key]);
          if (Number.isFinite(value) && value > 0) preferences[key] = value;
        },
      );
      return preferences;
    } catch {
      return {};
    }
  }

  function saveLayoutPreferences() {
    try {
      localStorage.setItem(
        LAYOUT_STORAGE_KEY,
        JSON.stringify(layoutPreferences),
      );
    } catch {
      // Layout persistence is optional when storage is unavailable.
    }
  }

  function isMobileWorkspace() {
    return window.matchMedia("(max-width: 760px)").matches;
  }

  function clamp(value, minimum, maximum) {
    return Math.min(maximum, Math.max(minimum, value));
  }

  function getSplitBounds(available, firstMinimum, secondMinimum, hardMaximum) {
    const total = Math.max(1, available);
    const adaptiveFloor = Math.max(80, Math.floor(total * 0.4));
    const minimum = Math.min(firstMinimum, adaptiveFloor);
    const trailingMinimum = Math.min(secondMinimum, adaptiveFloor);
    const maximum = Math.max(
      minimum,
      Math.min(hardMaximum || total, total - trailingMinimum),
    );
    return { minimum, maximum };
  }

  function getLayoutDefinition(kind) {
    if (kind === "sidebarWidth") {
      const available =
        els.appBody.clientWidth -
        els.sidebarResizer.getBoundingClientRect().width;
      const bounds = getSplitBounds(available, 260, 420, 520);
      return {
        ...bounds,
        current: els.sidebar.getBoundingClientRect().width,
        property: "--sidebar-width",
        resizer: els.sidebarResizer,
      };
    }

    if (kind === "queueHeight") {
      const available =
        els.sidebar.clientHeight -
        els.queueResizer.getBoundingClientRect().height;
      const minimum = isMobileWorkspace() ? 120 : 150;
      const bounds = getSplitBounds(available, minimum, minimum, available);
      return {
        ...bounds,
        current: els.queuePanel.getBoundingClientRect().height,
        property: "--queue-pane-size",
        resizer: els.queueResizer,
      };
    }

    if (kind === "resultHeight") {
      const available =
        els.workspace.clientHeight -
        els.workspaceResizer.getBoundingClientRect().height;
      const bounds = getSplitBounds(available, 140, 160, available);
      return {
        ...bounds,
        current: els.resultPane.getBoundingClientRect().height,
        property: "--mobile-result-size",
        resizer: els.workspaceResizer,
      };
    }

    const available =
      els.workspace.clientWidth -
      els.workspaceResizer.getBoundingClientRect().width;
    const bounds = getSplitBounds(available, 200, 320, 520);
    return {
      ...bounds,
      current: els.resultPane.getBoundingClientRect().width,
      property: "--result-width",
      resizer: els.workspaceResizer,
    };
  }

  function updateResizerAccessibility(definition, value) {
    const rounded = Math.round(value);
    definition.resizer.setAttribute(
      "aria-valuemin",
      String(Math.round(definition.minimum)),
    );
    definition.resizer.setAttribute(
      "aria-valuemax",
      String(Math.round(definition.maximum)),
    );
    definition.resizer.setAttribute("aria-valuenow", String(rounded));
    definition.resizer.setAttribute("aria-valuetext", `${rounded} 像素`);
  }

  function applyLayoutValue(kind, requestedValue, persist = false) {
    const definition = getLayoutDefinition(kind);
    const value = Math.round(
      clamp(
        Number(requestedValue) || definition.current,
        definition.minimum,
        definition.maximum,
      ),
    );
    document.documentElement.style.setProperty(
      definition.property,
      `${value}px`,
    );
    updateResizerAccessibility(definition, value);
    if (persist) {
      layoutPreferences[kind] = value;
      saveLayoutPreferences();
    }
    return value;
  }

  function currentLayoutValue(kind) {
    return getLayoutDefinition(kind).current;
  }

  function workspaceResizeKind() {
    return isMobileWorkspace() ? "resultHeight" : "resultWidth";
  }

  function updateWorkspaceResizerOrientation() {
    const mobile = isMobileWorkspace();
    els.workspaceResizer.classList.toggle("is-horizontal", mobile);
    els.workspaceResizer.classList.toggle("is-vertical", !mobile);
    els.workspaceResizer.setAttribute(
      "aria-orientation",
      mobile ? "horizontal" : "vertical",
    );
    els.workspaceResizer.setAttribute(
      "aria-label",
      mobile ? "调整结果导航与源码查看器高度" : "调整结果导航与源码查看器宽度",
    );
  }

  function syncResizableLayout() {
    updateWorkspaceResizerOrientation();

    if (!isMobileWorkspace()) {
      applyLayoutValue(
        "sidebarWidth",
        layoutPreferences.sidebarWidth || currentLayoutValue("sidebarWidth"),
      );
    }
    applyLayoutValue(
      "queueHeight",
      layoutPreferences.queueHeight || currentLayoutValue("queueHeight"),
    );

    const resultKind = workspaceResizeKind();
    applyLayoutValue(
      resultKind,
      layoutPreferences[resultKind] || currentLayoutValue(resultKind),
    );
  }

  function scheduleResizableLayoutSync() {
    if (document.body.classList.contains("is-resizing")) return;
    if (layoutSyncFrame) cancelAnimationFrame(layoutSyncFrame);
    layoutSyncFrame = requestAnimationFrame(() => {
      layoutSyncFrame = 0;
      if (document.body.classList.contains("is-resizing")) return;
      syncResizableLayout();
    });
  }

  function getResizerKind(resizer) {
    if (resizer === els.sidebarResizer) return "sidebarWidth";
    if (resizer === els.queueResizer) return "queueHeight";
    return workspaceResizeKind();
  }

  function setupPanelResizer(resizer) {
    let drag = null;

    const finishDrag = (event, cancelled) => {
      if (!drag || (event && event.pointerId !== drag.pointerId)) return;
      const activeDrag = drag;
      drag = null;
      if (cancelled) {
        applyLayoutValue(activeDrag.kind, activeDrag.startValue);
      } else {
        applyLayoutValue(
          activeDrag.kind,
          currentLayoutValue(activeDrag.kind),
          true,
        );
      }
      resizer.classList.remove("is-active");
      document.body.classList.remove(
        "is-resizing",
        "is-resizing-horizontal",
        "is-resizing-vertical",
      );
      scheduleResizableLayoutSync();
      if (
        event &&
        resizer.hasPointerCapture &&
        resizer.hasPointerCapture(event.pointerId)
      ) {
        resizer.releasePointerCapture(event.pointerId);
      }
    };

    resizer.addEventListener("pointerdown", (event) => {
      if (event.pointerType === "mouse" && event.button !== 0) return;
      const kind = getResizerKind(resizer);
      const horizontal = kind === "queueHeight" || kind === "resultHeight";
      drag = {
        pointerId: event.pointerId,
        kind,
        horizontal,
        startCoordinate: horizontal ? event.clientY : event.clientX,
        startValue: currentLayoutValue(kind),
      };
      resizer.setPointerCapture(event.pointerId);
      resizer.classList.add("is-active");
      document.body.classList.add(
        "is-resizing",
        horizontal ? "is-resizing-horizontal" : "is-resizing-vertical",
      );
      event.preventDefault();
    });

    resizer.addEventListener("pointermove", (event) => {
      if (!drag || event.pointerId !== drag.pointerId) return;
      const coordinate = drag.horizontal ? event.clientY : event.clientX;
      applyLayoutValue(
        drag.kind,
        drag.startValue + coordinate - drag.startCoordinate,
      );
      event.preventDefault();
    });
    resizer.addEventListener("pointerup", (event) => finishDrag(event, false));
    resizer.addEventListener("pointercancel", (event) =>
      finishDrag(event, true),
    );
    resizer.addEventListener("lostpointercapture", (event) =>
      finishDrag(event, false),
    );

    resizer.addEventListener("keydown", (event) => {
      const kind = getResizerKind(resizer);
      const horizontal = kind === "queueHeight" || kind === "resultHeight";
      const definition = getLayoutDefinition(kind);
      const step = RESIZE_KEYBOARD_STEP * (event.shiftKey ? 3 : 1);
      let nextValue = null;

      if (event.key === "Home") nextValue = definition.minimum;
      else if (event.key === "End") nextValue = definition.maximum;
      else if (!horizontal && event.key === "ArrowLeft")
        nextValue = definition.current - step;
      else if (!horizontal && event.key === "ArrowRight")
        nextValue = definition.current + step;
      else if (horizontal && event.key === "ArrowUp")
        nextValue = definition.current - step;
      else if (horizontal && event.key === "ArrowDown")
        nextValue = definition.current + step;

      if (nextValue === null) return;
      event.preventDefault();
      applyLayoutValue(kind, nextValue, true);
    });
  }

  function setupResizableLayout() {
    [els.sidebarResizer, els.queueResizer, els.workspaceResizer].forEach(
      setupPanelResizer,
    );
    window.addEventListener("resize", scheduleResizableLayoutSync, {
      passive: true,
    });
    window
      .matchMedia("(max-width: 760px)")
      .addEventListener("change", scheduleResizableLayoutSync);

    if (typeof ResizeObserver === "function") {
      layoutResizeObserver = new ResizeObserver(scheduleResizableLayoutSync);
      layoutResizeObserver.observe(els.appBody);
      layoutResizeObserver.observe(els.sidebar);
      layoutResizeObserver.observe(els.workspace);
    }
    scheduleResizableLayoutSync();
  }

  function cacheElements() {
    Object.assign(els, {
      appBody: $("#app-body"),
      workspace: $(".workspace"),
      sourceDir: $("#source-dir"),
      outputDir: $("#output-dir"),
      filterPackage: $("#filter-package"),
      filterField: $("#filter-field"),
      btnBrowseSource: $("#btn-browse-source"),
      btnBrowseOutput: $("#btn-browse-output"),
      btnScan: $("#btn-scan"),
      btnRun: $("#btn-run"),
      modeDecompile: $("#mode-decompile"),
      modeExtract: $("#mode-extract"),
      btnSettings: $("#btn-settings"),
      btnTaskLog: $("#btn-task-log"),
      btnSidebar: $("#btn-sidebar"),
      btnCloseSidebar: $("#btn-close-sidebar"),
      sidebarBackdrop: $("#sidebar-backdrop"),
      sidebar: $("#sidebar"),
      sidebarResizer: $("#sidebar-resizer"),
      queuePanel: $("#queue-panel"),
      queueResizer: $("#queue-resizer"),
      btnSelectAll: $("#btn-select-all"),
      jarCount: $("#jar-count"),
      jarList: $("#jar-list"),
      queueEmpty: $("#queue-empty"),
      btnRefreshAnalysis: $("#btn-refresh-analysis"),
      analysisEmpty: $("#analysis-empty"),
      analysisContent: $("#analysis-content"),
      analysisName: $("#analysis-name"),
      analysisPath: $("#analysis-path"),
      analysisState: $("#analysis-state"),
      analysisTotal: $("#analysis-total"),
      analysisClasses: $("#analysis-classes"),
      analysisResources: $("#analysis-resources"),
      analysisPackageCount: $("#analysis-package-count"),
      analysisPackages: $("#analysis-packages"),
      analysisManifest: $("#analysis-manifest"),
      taskProgress: $("#task-progress"),
      taskProgressTitle: $("#task-progress-title"),
      taskProgressDetail: $("#task-progress-detail"),
      taskProgressPercent: $("#task-progress-percent"),
      taskProgressBar: $("#task-progress-bar"),
      btnTaskCancel: $("#btn-task-cancel"),
      btnTaskDismiss: $("#btn-task-dismiss"),
      resultFilesTab: $("#result-files-tab"),
      resultSearchTab: $("#result-search-tab"),
      resultPane: $("#result-pane"),
      workspaceResizer: $("#workspace-resizer"),
      editorArea: $("#editor-area"),
      filesPanel: $("#files-panel"),
      searchPanel: $("#search-panel"),
      fileTree: $("#file-tree"),
      treeEmpty: $("#tree-empty"),
      searchForm: $("#search-form"),
      searchKeyword: $("#search-keyword"),
      searchIgnoreCase: $("#search-ignore-case"),
      btnSearch: $("#btn-search"),
      searchState: $("#search-state"),
      searchResults: $("#search-results"),
      tabBar: $("#tab-bar"),
      codeViewer: $("#code-viewer"),
      statusText: $("#status-text"),
      statusContext: $("#status-context"),
      browseDialog: $("#browse-dialog"),
      browseTitle: $("#browse-title"),
      browseCurrentDir: $("#browse-current-dir"),
      browseUp: $("#browse-up"),
      browseGo: $("#browse-go"),
      browseSelectDir: $("#browse-select-dir"),
      browseState: $("#browse-state"),
      browseList: $("#browse-list"),
      settingsDialog: $("#settings-dialog"),
      settingsForm: $("#settings-form"),
      settingJavaPath: $("#setting-java-path"),
      settingCfrPath: $("#setting-cfr-path"),
      settingsState: $("#settings-state"),
      settingsReset: $("#settings-reset"),
      settingsSave: $("#settings-save"),
      logDialog: $("#log-dialog"),
      taskLogContent: $("#task-log-content"),
      dragOverlay: $("#drag-overlay"),
      toastContainer: $("#toast-container"),
    });
  }

  function bindEvents() {
    setupDialog(els.browseDialog);
    setupDialog(els.settingsDialog);
    setupDialog(els.logDialog);
    setupDragAndDrop();

    els.btnSidebar.addEventListener("click", () =>
      setSidebarOpen(!document.body.classList.contains("sidebar-open")),
    );
    els.btnCloseSidebar.addEventListener("click", () => setSidebarOpen(false));
    els.sidebarBackdrop.addEventListener("click", () => setSidebarOpen(false));

    els.btnBrowseSource.addEventListener("click", () => openBrowse("source"));
    els.btnBrowseOutput.addEventListener("click", () => openBrowse("output"));
    els.btnScan.addEventListener("click", scanDir);
    els.sourceDir.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        scanDir();
      }
    });
    els.sourceDir.addEventListener("input", () => {
      if (
        state.scanSession &&
        els.sourceDir.value.trim() !== state.scanSession.inputDir
      ) {
        invalidateScanSession("源目录已更改，请重新扫描");
      }
    });

    els.btnSelectAll.addEventListener("click", () => {
      const allSelected =
        state.jars.length > 0 && state.jars.every((jar) => jar.checked);
      state.jars.forEach((jar) => {
        jar.checked = !allSelected;
      });
      renderJarList();
    });
    els.btnRefreshAnalysis.addEventListener("click", () => {
      const jar = state.jars.find((item) => item.path === state.activeJarPath);
      if (jar) loadJarAnalysis(jar, true);
    });

    els.modeDecompile.addEventListener("click", () => setMode("decompile"));
    els.modeExtract.addEventListener("click", () => setMode("extract"));
    els.btnRun.addEventListener("click", runSelectedTask);
    els.btnTaskCancel.addEventListener("click", cancelActiveTask);
    els.btnTaskDismiss.addEventListener("click", () => {
      els.taskProgress.hidden = true;
    });

    els.resultFilesTab.addEventListener("click", () =>
      activateResultTab("files"),
    );
    els.resultSearchTab.addEventListener("click", () =>
      activateResultTab("search"),
    );
    $(".result-tabs").addEventListener("keydown", handleResultTabKeydown);
    els.fileTree.addEventListener("keydown", handleTreeKeydown);
    els.tabBar.addEventListener("keydown", handleTabKeydown);
    els.searchForm.addEventListener("submit", (event) => {
      event.preventDefault();
      doSearch();
    });

    els.browseUp.addEventListener("click", () => {
      if (state.browseParent) browseTo(state.browseParent);
    });
    els.browseGo.addEventListener("click", () =>
      browseTo(els.browseCurrentDir.value.trim()),
    );
    els.browseCurrentDir.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        browseTo(els.browseCurrentDir.value.trim());
      }
    });
    els.browseSelectDir.addEventListener("click", selectBrowsedDirectory);

    els.btnSettings.addEventListener("click", loadSettings);
    els.settingsForm.addEventListener("submit", (event) => {
      event.preventDefault();
      saveSettings();
    });
    els.settingsReset.addEventListener("click", () => {
      els.settingJavaPath.value = "";
      els.settingCfrPath.value = "";
      els.settingsState.textContent = "保存后恢复自动检测";
      els.settingJavaPath.focus();
    });
    els.btnTaskLog.addEventListener("click", () => {
      els.taskLogContent.textContent = state.lastTaskLog || "暂无任务日志";
      openDialog(els.logDialog, ".dialog-close");
    });

    document.addEventListener("keydown", handleGlobalKeydown);
    window
      .matchMedia("(min-width: 761px)")
      .addEventListener("change", (event) => {
        if (event.matches) setSidebarOpen(false);
      });
  }

  function init() {
    cacheElements();
    setupResizableLayout();
    bindEvents();
    setMode("decompile");
    renderJarList();
    renderAnalysisEmpty();
    saveTaskLog("");
    refreshIcons();
  }

  document.addEventListener("DOMContentLoaded", init);
})();
