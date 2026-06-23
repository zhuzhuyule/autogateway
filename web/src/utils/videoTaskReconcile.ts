import type { VideoTask } from "@/api/videoTasks";

export interface ReconcilableMessage {
  videoTaskId?: string;
  phase?: "thinking" | "streaming" | "done";
  content: string;
  error?: boolean;
}

// reconcileMessage 根据后端任务状态原地更新一条消息。返回是否发生变化。
// 文案插值交给调用方(i18n);这里只负责状态机映射。
export function reconcileMessage(
  msg: ReconcilableMessage,
  task: VideoTask,
  texts: { generating: (p: number) => string; failed: string; timeout: string },
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
      msg.content = texts.generating(task.progress ?? 0);
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
