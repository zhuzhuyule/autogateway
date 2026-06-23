// web/src/api/videoTasks.ts
export interface VideoTask {
  id: string;
  group_name: string;
  model: string;
  prompt: string;
  status: "pending" | "running" | "completed" | "failed" | "canceled";
  upstream_task_id: string;
  video_url: string;
  progress: number;
  error: string;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
}

function authHeaders(authKey: string): HeadersInit {
  return { Authorization: `Bearer ${authKey}`, "Content-Type": "application/json" };
}

export async function createVideoTask(
  authKey: string,
  body: { group: string; model: string; prompt: string; params?: Record<string, unknown> },
): Promise<VideoTask> {
  const resp = await fetch("/api/video-tasks", {
    method: "POST",
    headers: authHeaders(authKey),
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`create video task failed: ${resp.status}`);
  return resp.json();
}

export async function getVideoTasksByIds(authKey: string, ids: string[]): Promise<VideoTask[]> {
  if (ids.length === 0) return [];
  const resp = await fetch(`/api/video-tasks?ids=${encodeURIComponent(ids.join(","))}`, {
    headers: authHeaders(authKey),
  });
  if (!resp.ok) throw new Error(`list video tasks failed: ${resp.status}`);
  const data = await resp.json();
  return data.tasks ?? [];
}

export async function listVideoTasks(
  authKey: string,
  opts: { status?: string; page?: number; pageSize?: number } = {},
): Promise<{ tasks: VideoTask[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set("status", opts.status);
  p.set("page", String(opts.page ?? 1));
  p.set("page_size", String(opts.pageSize ?? 20));
  const resp = await fetch(`/api/video-tasks?${p.toString()}`, { headers: authHeaders(authKey) });
  if (!resp.ok) throw new Error(`list video tasks failed: ${resp.status}`);
  return resp.json();
}

export async function cancelVideoTask(authKey: string, id: string): Promise<void> {
  await fetch(`/api/video-tasks/${id}/cancel`, { method: "POST", headers: authHeaders(authKey) });
}
export async function retryVideoTask(authKey: string, id: string): Promise<VideoTask> {
  const resp = await fetch(`/api/video-tasks/${id}/retry`, { method: "POST", headers: authHeaders(authKey) });
  if (!resp.ok) throw new Error(`retry failed: ${resp.status}`);
  return resp.json();
}
export async function deleteVideoTask(authKey: string, id: string): Promise<void> {
  await fetch(`/api/video-tasks/${id}`, { method: "DELETE", headers: authHeaders(authKey) });
}
