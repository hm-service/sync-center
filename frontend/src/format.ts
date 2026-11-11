export function formatSize(bytes: number): string {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / 1024 / 1024).toFixed(1) + " MB";
}

export function formatTime(seconds: number, language: string): string {
  return new Date(seconds * 1000).toLocaleString(language === "zh" ? "zh-CN" : "en-US");
}

// 文件行的 mtime 是毫秒，用固定宽度格式，避免每行长度跳动。
export function formatStamp(milliseconds: number): string {
  const date = new Date(milliseconds);
  const pad = (value: number) => String(value).padStart(2, "0");
  return (
    date.getFullYear() +
    "-" + pad(date.getMonth() + 1) +
    "-" + pad(date.getDate()) +
    " " + pad(date.getHours()) +
    ":" + pad(date.getMinutes())
  );
}

const textExtensions = ["yaml", "yml", "txt", "json", "md", "log", "lua", "toml", "ini", "conf"];

export function isText(path: string): boolean {
  const dot = path.lastIndexOf(".");
  if (dot < 0) return false;
  return textExtensions.indexOf(path.slice(dot + 1).toLowerCase()) >= 0;
}

export const previewLimit = 256 * 1024;
