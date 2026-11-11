const MESSAGES = {
  "ui.readonly": { zh: "只读浏览", en: "read-only" },
  "ui.namespaces": { zh: "命名空间", en: "Namespaces" },
  "ui.snapshots": { zh: "快照", en: "Snapshots" },
  "ui.files": { zh: "文件", en: "Files" },
  "ui.preview": { zh: "预览", en: "Preview" },
  "ui.updated-at": { zh: "更新于 {time}", en: "updated {time}" },
  "ui.select-namespace": { zh: "先选一个 namespace", en: "select a namespace first" },
  "ui.select-snapshot": { zh: "先选一个 snapshot", en: "select a snapshot first" },
  "ui.no-namespaces": { zh: "没有 namespace", en: "no namespaces" },
  "ui.no-snapshots": { zh: "没有快照", en: "no snapshots" },
  "ui.empty-snapshot": { zh: "空快照", en: "empty snapshot" },
  "ui.click-file": { zh: "点一个文件查看", en: "click a file to preview" },
  "ui.preview-too-large": { zh: "{size} · 超过 256 KB，不预览", en: "{size} · larger than 256 KB, not previewed" },
  "ui.download": { zh: "下载 {path}", en: "download {path}" },
  "ui.binary-file": { zh: "{size} · 二进制文件", en: "{size} · binary file" },
  "ui.file-count": { zh: "{count} 个文件", en: "{count} files" },
  "ui.load-failed": { zh: "加载失败", en: "load failed" },
  "ui.need-login": { zh: "需要登录", en: "authentication required" },
  "ui.login-title": { zh: "输入访问口令", en: "enter the access token" },
  "ui.login-submit": { zh: "进入", en: "sign in" },
  "ui.login-invalid": { zh: "口令不正确", en: "invalid token" },
  "ui.login-failed": { zh: "登录失败：{status}", en: "sign-in failed: {status}" },
  "ui.login-offline": { zh: "无法连接服务", en: "cannot reach the service" },
};

const SUPPORTED = ["zh", "en"];

function detectLanguage() {
  const saved = localStorage.getItem("lang");
  if (SUPPORTED.indexOf(saved) >= 0) return saved;
  return (navigator.language || "en").toLowerCase().startsWith("zh") ? "zh" : "en";
}

let currentLanguage = detectLanguage();

function t(key, params) {
  const entry = MESSAGES[key];
  if (!entry) return key;
  let text = entry[currentLanguage] || entry.en;
  if (params) {
    Object.keys(params).forEach((name) => {
      text = text.replaceAll("{" + name + "}", params[name]);
    });
  }
  return text;
}

function apply(root) {
  const scope = root || document;
  scope.querySelectorAll("[data-i18n]").forEach((el) => {
    el.textContent = t(el.dataset.i18n);
  });
  scope.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
    el.placeholder = t(el.dataset.i18nPlaceholder);
  });
  document.documentElement.lang = currentLanguage;
}

function setLanguage(language) {
  if (SUPPORTED.indexOf(language) < 0) return;
  currentLanguage = language;
  localStorage.setItem("lang", language);
  apply();
}

window.I18N = {
  t,
  apply,
  setLanguage,
  get lang() {
    return currentLanguage;
  },
};
