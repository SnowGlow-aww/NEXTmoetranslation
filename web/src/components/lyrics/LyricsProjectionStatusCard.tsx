import type { ProjectionStatus, SongProvenance } from "@/lib/api";

export function songProvenanceLabel(provenance: SongProvenance): string {
  const sourceNames: Record<string, string> = {
    bundle: "内置发布包",
    db_publication: "数据库发布",
    localization_projection: "本地化编辑投影",
    generated: "动态生成",
  };
  const src = sourceNames[provenance.source] || provenance.source;
  return `当前公开来源：${src} · Revision ${provenance.revision}`;
}

export interface LyricsProjectionStatusCardProps {
  projectionState: "idle" | "checking" | "ready" | "failed" | "unknown";
  projectionStatus: ProjectionStatus | null;
  projectionMessage: string;
  busy: boolean;
  onRefresh: () => void;
}

export function LyricsProjectionStatusCard({
  projectionState,
  projectionStatus,
  projectionMessage,
  busy,
  onRefresh,
}: LyricsProjectionStatusCardProps) {
  if (projectionState === "idle") return null;

  return (
    <div
      className={`lyrics-projection-state ${projectionState}`}
      role={projectionState === "failed" ? "alert" : "status"}
      aria-live={projectionState === "failed" ? "assertive" : "polite"}
    >
      <div>
        <strong>公共文件状态</strong>
        {projectionStatus && (
          <span>
            generation {projectionStatus.generation}
            {projectionStatus.pending ? " · 生成中" : ""}
          </span>
        )}
      </div>
      <p>{projectionMessage}</p>
      {projectionStatus?.song && (
        <p className="lyrics-provenance-info">{songProvenanceLabel(projectionStatus.song)}</p>
      )}
      {projectionStatus?.lyricsSummary?.degraded && (
        <p className="lyrics-degraded-warning">
          ⚠️ 歌词公共文件处于降级服务状态
          {projectionStatus.lyricsSummary.degradedReason ? `（${projectionStatus.lyricsSummary.degradedReason}）` : ""}，当前由基础发布包供内容。
        </p>
      )}
      <button
        type="button"
        className="btn btn-secondary btn-sm"
        onClick={onRefresh}
        disabled={busy || projectionState === "checking"}
      >
        重新核对公共文件
      </button>
    </div>
  );
}
