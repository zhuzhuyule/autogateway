import type { VideoTask } from "@/api/videoTasks";

export interface ReconcilableMessage {
  videoTaskId?: string;
  phase?: "thinking" | "streaming" | "done";
  content: string;
  error?: boolean;
}

// fmtElapsed 把秒数格式化为 "45s" / "3:07", 供"已等待时长"展示复用。
export function fmtElapsed(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) sec = 0;
  sec = Math.floor(sec);
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return m > 0 ? `${m}:${String(s).padStart(2, "0")}` : `${s}s`;
}

// elapsedSeconds 根据任务 started_at(优先)/created_at 算已耗时(秒)。
// agnes 的 POST 同步阻塞、不报中间进度, 所以用"已等待时长"代替无意义的 0%。
export function elapsedSeconds(task: VideoTask, nowMs: number): number {
  const base = task.started_at || task.created_at;
  if (!base) return 0;
  const t = Date.parse(base);
  if (Number.isNaN(t)) return 0;
  return Math.max(0, Math.floor((nowMs - t) / 1000));
}

// reconcileMessage 根据后端任务状态原地更新一条消息。返回是否发生变化。
// 文案插值交给调用方(i18n);这里只负责状态机映射。
// nowMs 用于 pending/running 计算已等待时长。
export function reconcileMessage(
  msg: ReconcilableMessage,
  task: VideoTask,
  texts: { generating: (elapsedSec: number) => string; failed: string },
  nowMs: number,
): boolean {
  if (msg.phase === "done") return false;
  switch (task.status) {
    case "completed":
      if (task.video_url) {
        msg.content = `![](${task.video_url})`;
        msg.phase = "done";
        return true;
      }
      return false;
    case "failed":
      msg.content = task.error ? `${texts.failed} (${task.error})` : texts.failed;
      msg.error = true;
      msg.phase = "done";
      return true;
    case "canceled":
      msg.content = texts.failed;
      msg.error = true;
      msg.phase = "done";
      return true;
    case "pending":
    case "running":
    default:
      msg.content = texts.generating(elapsedSeconds(task, nowMs));
      return true;
  }
}

// collectPendingTaskIds 从所有会话消息里收集仍需轮询的 videoTaskId。
export function collectPendingTaskIds(
  sessions: { messages: ReconcilableMessage[] }[],
): string[] {
  const ids: string[] = [];
  for (const s of sessions) {
    for (const m of s.messages) {
      if (m.videoTaskId && m.phase !== "done") ids.push(m.videoTaskId);
    }
  }
  return ids;
}
