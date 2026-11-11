export type Namespace = { name: string; createdAt: number; updatedAt: number };
export type SnapshotMeta = { seq: number; createdAt: number; totalBytes: number };
export type Entry = { path: string; hash: string; size: number; mode: number; mtime: number };
export type Snapshot = { seq: number; createdAt: number; totalBytes: number; manifest: { files: Entry[] } };

export async function fetchOrLogin(path: string): Promise<Response> {
  const response = await fetch(path);
  if (response.status === 401) {
    location.href = "/auth";
    throw new Error("unauthorized");
  }
  return response;
}

export async function api<T>(path: string): Promise<T> {
  const response = await fetchOrLogin(path);
  if (!response.ok) throw new Error(await response.text());
  return (await response.json()) as T;
}
