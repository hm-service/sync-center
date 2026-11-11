import { ref } from "vue";

type Entry = { zh: string; en: string };

export const messages = {
  "ui.title": { zh: "sync-center", en: "sync-center" },
  "ui.readonly": { zh: "只读浏览", en: "read-only" },
  "ui.namespaces": { zh: "命名空间", en: "Namespaces" },
  "ui.snapshots": { zh: "快照", en: "Snapshots" },
  "ui.files": { zh: "文件", en: "Files" },
  "ui.preview": { zh: "预览", en: "Preview" },
  "ui.updated-at": { zh: "更新于 {time}", en: "updated {time}" },
  "ui.select-namespace": { zh: "先选一个命名空间", en: "select a namespace first" },
  "ui.select-snapshot": { zh: "先选一个快照", en: "select a snapshot first" },
  "ui.no-namespaces": { zh: "没有命名空间", en: "no namespaces" },
  "ui.no-snapshots": { zh: "没有快照", en: "no snapshots" },
  "ui.empty-snapshot": { zh: "空快照", en: "empty snapshot" },
  "ui.click-file": { zh: "点一个文件查看", en: "click a file to preview" },
  "ui.preview-too-large": { zh: "{size} · 超过 256 KB，不预览", en: "{size} · larger than 256 KB, not previewed" },
  "ui.download": { zh: "下载 {path}", en: "download {path}" },
  "ui.binary-file": { zh: "{size} · 二进制文件", en: "{size} · binary file" },
  "ui.file-count": { zh: "{count} 个文件", en: "{count} files" },
  "ui.load-failed": { zh: "加载失败", en: "load failed" },
  "ui.need-login": { zh: "需要登录", en: "authentication required" },
  "ui.language": { zh: "语言", en: "Language" },
  "ui.login-title": { zh: "输入访问口令", en: "enter the access token" },
  "ui.login-submit": { zh: "进入", en: "sign in" },
  "ui.login-invalid": { zh: "口令不正确", en: "invalid token" },
  "ui.login-failed": { zh: "登录失败：{status}", en: "sign-in failed: {status}" },
  "ui.login-offline": { zh: "无法连接服务", en: "cannot reach the service" },
} satisfies Record<string, Entry>;

export type MessageKey = keyof typeof messages;

export const languages = ["zh", "en"] as const;
export type Language = (typeof languages)[number];

const storageKey = "lang";
const current = ref<Language>(detect());

function detect(): Language {
  const saved = localStorage.getItem(storageKey);
  if (saved === "zh" || saved === "en") return saved;
  return (navigator.language || "en").toLowerCase().startsWith("zh") ? "zh" : "en";
}

export function t(key: MessageKey, params?: Record<string, string | number>): string {
  const entry = messages[key];
  let text: string = entry[current.value];
  if (params) {
    for (const [name, value] of Object.entries(params)) {
      text = text.replaceAll("{" + name + "}", String(value));
    }
  }
  return text;
}

export function setLanguage(next: Language): void {
  current.value = next;
  localStorage.setItem(storageKey, next);
  document.documentElement.lang = next;
}

export function useLanguage() {
  return { current, setLanguage, t };
}

document.documentElement.lang = current.value;
