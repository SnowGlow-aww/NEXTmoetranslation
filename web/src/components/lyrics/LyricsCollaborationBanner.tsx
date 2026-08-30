import type { LyricsCollaborationPeer, LyricsCollaborationStatus } from "@/lib/yjs-lyrics";

export interface LyricsCollaborationBannerProps {
  collaborationStructuralConflict: boolean;
  localSourceImportDraft: boolean;
  collaborationStatus: LyricsCollaborationStatus;
  collaborationError: string;
  collaborationPeers: LyricsCollaborationPeer[];
  busy: boolean;
  onReload: () => void;
  onReconnect: () => void;
}

export function LyricsCollaborationBanner({
  collaborationStructuralConflict,
  localSourceImportDraft,
  collaborationStatus,
  collaborationError,
  collaborationPeers,
  busy,
  onReload,
  onReconnect,
}: LyricsCollaborationBannerProps) {
  return (
    <div
      className={`lyrics-collaboration-state ${collaborationStructuralConflict ? "structural-conflict" : collaborationStatus}`}
      role={collaborationStructuralConflict || collaborationStatus === "error" ? "alert" : "status"}
      aria-live="polite"
    >
      <div>
        <strong>
          {collaborationStructuralConflict
            ? "协作文档发生结构冲突，已切换为只读"
            : localSourceImportDraft
            ? "固定来源草稿尚未共享"
            : collaborationStatus === "synced"
            ? "协作同步已就绪"
            : collaborationStatus === "reconnecting"
            ? "协作连接正在恢复"
            : collaborationStatus === "error"
            ? "协作连接不可用"
            : "正在载入协作文档"}
        </strong>
        <span>
          {collaborationStructuralConflict
            ? "Yjs 已完成同步，但歌词的行、分段或注音结构无法安全物化。为避免覆盖协作者内容，编辑与保存已停用。"
            : localSourceImportDraft
            ? "首次受权保存成功后才会进入共享房间"
            : collaborationStatus === "synced"
            ? "正文由 Yjs 增量同步，保存会创建数据库 checkpoint"
            : "首轮远端同步完成前保持只读，避免空草稿覆盖权威内容"}
        </span>
        {collaborationStructuralConflict ? (
          <small>请重新加载当前歌词；若重新加载后仍出现此状态，请联系其他协作者停止编辑，并由管理员检查协作文档。</small>
        ) : (
          collaborationError && <small>{collaborationError}</small>
        )}
      </div>
      {!collaborationStructuralConflict && !localSourceImportDraft && collaborationPeers.length > 0 && (
        <ul aria-label="其他在线歌词编辑者">
          {collaborationPeers.map((peer) => (
            <li key={peer.clientId}>
              <i aria-hidden="true" style={{ backgroundColor: peer.color }} />
              {peer.username}
            </li>
          ))}
        </ul>
      )}
      {collaborationStructuralConflict && <button type="button" className="btn btn-secondary btn-sm" onClick={onReload} disabled={busy}>重新加载歌词</button>}
      {!collaborationStructuralConflict && !localSourceImportDraft && collaborationStatus === "error" && <button type="button" className="btn btn-secondary btn-sm" onClick={onReconnect}>重新连接</button>}
    </div>
  );
}
