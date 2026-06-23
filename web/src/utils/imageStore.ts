// 基于 IndexedDB 的图片 dataURL 持久化 KV。
//
// 聊天附件是 base64 dataURL, 动辄几 MB, 放进 localStorage(5MB 上限)会撑爆,
// 所以 Playground 此前持久化时直接 strip 掉图片 —— 刷新/重开聊天后历史图片就
// 丢了。这里按 attachment id 把 dataURL 存进 IndexedDB(配额远大于 localStorage),
// localStorage 只保留附件元数据(id/name/mime), 加载时再 hydrate 回 dataURL。
//
// IndexedDB 在隐私模式等场景可能不可用 —— 所有函数失败时由调用方降级处理
// (图片不持久化, 但不影响聊天主流程)。

const DB_NAME = "playground-images";
const STORE = "attachments";
const VERSION = 1;

let dbPromise: Promise<IDBDatabase> | null = null;

function openDB(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, VERSION);
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) {
        req.result.createObjectStore(STORE);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return dbPromise;
}

// putImage 存一张图片(key=attachment id, value=dataURL)。
export async function putImage(id: string, dataUrl: string): Promise<void> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    tx.objectStore(STORE).put(dataUrl, id);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

// getImages 批量取 dataURL, 返回 id→dataUrl 的 Map(缺失的 id 不在 Map 中)。
export async function getImages(ids: string[]): Promise<Map<string, string>> {
  const out = new Map<string, string>();
  if (!ids.length) return out;
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readonly");
    const store = tx.objectStore(STORE);
    for (const id of ids) {
      const r = store.get(id);
      r.onsuccess = () => {
        if (typeof r.result === "string") out.set(id, r.result);
      };
    }
    tx.oncomplete = () => resolve(out);
    tx.onerror = () => reject(tx.error);
  });
}

// deleteImages 删除若干图片(删除会话时清理, 避免 IndexedDB 无限增长)。
export async function deleteImages(ids: string[]): Promise<void> {
  if (!ids.length) return;
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    const store = tx.objectStore(STORE);
    for (const id of ids) {
      store.delete(id);
    }
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}
