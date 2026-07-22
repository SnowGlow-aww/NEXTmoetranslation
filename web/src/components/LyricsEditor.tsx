"use client";

import { useEffect, useRef, useState } from "react";
import { useToast } from "@/app/providers";
import {
  APIError, CatalogMusicItem, CatalogPerformerItem, LyricsSourceCandidate,
  LyricsSourcePreview, SongLyrics, getCatalogMusic, getCatalogPerformers,
  getLyrics, previewLyricsSource, publishLyrics, saveLyrics,
  searchLyricsSource, unpublishLyrics,
} from "@/lib/api";

function emptyLyrics(musicId: number): SongLyrics {
  return { musicId, status: "draft", revision: 0, updatedAt: "", lines: [] };
}

function sourceLabel(error: APIError): string {
  const labels: Record<string, string> = {
    revision_conflict: "其他编辑者已保存新版本",
    segment_mismatch: "分段文字与日文原文不一致",
    invalid_performer: "包含无效的演唱者",
    incomplete_publication: "发布前必须补齐中英翻译",
    source_drift: "歌词来源或日文原文已变化",
    source_restricted: "来源页面禁止转载",
    source_unsupported: "无法安全解析来源页面",
    source_identity_mismatch: "来源页面与曲目资料不匹配",
    source_identity_incomplete: "曲目缺少用于核对来源的作者资料",
    source_unavailable: "歌词来源暂时不可用",
  };
  return labels[error.code] || error.message;
}

export function LyricsEditor({ role }: { role: "admin" | "editor" | "" }) {
  const { show } = useToast();
  const [query, setQuery] = useState("");
  const [catalog, setCatalog] = useState<CatalogMusicItem[]>([]);
  const [performers, setPerformers] = useState<CatalogPerformerItem[]>([]);
  const [selectedMusic, setSelectedMusic] = useState<CatalogMusicItem | null>(null);
  const [lyrics, setLyrics] = useState<SongLyrics | null>(null);
  const [baseline, setBaseline] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<APIError | null>(null);
  const [candidates, setCandidates] = useState<LyricsSourceCandidate[]>([]);
  const [sourcePreview, setSourcePreview] = useState<LyricsSourcePreview | null>(null);
  const [previewLocale, setPreviewLocale] = useState<"ja-JP" | "zh-CN" | "en-US">("zh-CN");
  const requestSequence = useRef(0);

  const dirty = lyrics != null && JSON.stringify(lyrics) !== baseline;

  useEffect(() => {
    const timer = window.setTimeout(() => {
      const sequence = ++requestSequence.current;
      getCatalogMusic(query).then((result) => {
        if (requestSequence.current === sequence) setCatalog(result.items);
      }).catch((reason) => show(reason instanceof Error ? reason.message : "曲目目录加载失败", "err"));
    }, 200);
    return () => window.clearTimeout(timer);
  }, [query, show]);

  useEffect(() => {
    getCatalogPerformers().then((result) => setPerformers(result.items))
      .catch((reason) => show(reason instanceof Error ? reason.message : "演唱者目录加载失败", "err"));
  }, [show]);

  const chooseMusic = async (item: CatalogMusicItem) => {
    if (dirty && !window.confirm("当前歌词有未保存修改，放弃并切换曲目？")) return;
    setSelectedMusic(item);
    setLoading(true);
    setError(null);
    setCandidates([]);
    setSourcePreview(null);
    try {
      const loaded = await getLyrics(item.musicId);
      setLyrics(loaded);
      setBaseline(JSON.stringify(loaded));
    } catch (reason) {
      if (reason instanceof APIError && reason.status === 404) {
        const blank = emptyLyrics(item.musicId);
        setLyrics(blank);
        setBaseline(JSON.stringify(blank));
      } else {
        setLyrics(null);
        setError(reason instanceof APIError ? reason : new APIError(500, { error: "load_failed" }));
      }
    } finally {
      setLoading(false);
    }
  };

  const updateLyrics = (patch: Partial<SongLyrics>) => {
    setLyrics((current) => current ? { ...current, ...patch } : current);
    setError(null);
  };

  const updateLine = (index: number, patch: Partial<SongLyrics["lines"][number]>) => {
    if (!lyrics) return;
    const lines = lyrics.lines.map((line, lineIndex) => lineIndex === index ? { ...line, ...patch } : line);
    updateLyrics({ lines });
  };

  const updateSegment = (lineIndex: number, segmentIndex: number, text: string, performerIds?: number[]) => {
    if (!lyrics) return;
    const line = lyrics.lines[lineIndex];
    const segments = line.segments.map((segment, index) => index === segmentIndex
      ? { ...segment, ...(performerIds ? { performerIds } : { text }) }
      : segment);
    updateLine(lineIndex, { segments, japanese: segments.map((segment) => segment.text).join("") });
  };

  const addLine = () => {
    if (!lyrics || lyrics.revision > 0) return;
    const order = lyrics.lines.length;
    updateLyrics({ lines: [...lyrics.lines, {
      id: `manual-${lyrics.musicId}-${Date.now()}-${order}`,
      order, japanese: "", "zh-CN": "", "en-US": "", segments: [{ text: "", performerIds: [] }],
    }] });
  };

  const save = async () => {
    if (!lyrics || busy) return;
    setBusy(true);
    setError(null);
    try {
      const saved = await saveLyrics(lyrics);
      setLyrics(saved);
      setBaseline(JSON.stringify(saved));
      show("歌词草稿已保存", "ok");
    } catch (reason) {
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "save_failed" });
      setError(apiError);
      show(sourceLabel(apiError), "err");
    } finally {
      setBusy(false);
    }
  };

  const publish = async (nextPublished: boolean) => {
    if (!lyrics || busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = nextPublished
        ? await publishLyrics(lyrics.musicId, lyrics.revision)
        : await unpublishLyrics(lyrics.musicId, lyrics.revision);
      setLyrics(result);
      setBaseline(JSON.stringify(result));
      show(nextPublished ? "歌词已发布" : "歌词已取消发布", "ok");
    } catch (reason) {
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "publication_failed" });
      setError(apiError);
      show(sourceLabel(apiError), "err");
    } finally {
      setBusy(false);
    }
  };

  const findSource = async () => {
    if (!lyrics || busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = await searchLyricsSource(lyrics.musicId);
      setCandidates(result.items);
      if (result.items.length === 0) show("没有找到可核对的歌词来源", "err");
    } catch (reason) {
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "source_unavailable" });
      setError(apiError);
      show(sourceLabel(apiError), "err");
    } finally {
      setBusy(false);
    }
  };

  const previewSource = async (candidate: LyricsSourceCandidate) => {
    if (!lyrics || busy) return;
    setBusy(true);
    try {
      setSourcePreview(await previewLyricsSource(lyrics.musicId, candidate.pageId, candidate.revisionId));
    } catch (reason) {
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "source_unavailable" });
      setError(apiError);
      show(sourceLabel(apiError), "err");
    } finally {
      setBusy(false);
    }
  };

  const acceptPreview = () => {
    if (!lyrics || !sourcePreview) return;
    const lines = sourcePreview.lines.map((line, order) => ({
      id: `wiki-${sourcePreview.pageId}-${sourcePreview.revisionId}-${order + 1}`,
      order, japanese: line.japanese, "zh-CN": "", "en-US": "",
      stanzaBreakBefore: line.stanzaBreakBefore,
      segments: [{ text: line.japanese, performerIds: [] }],
    }));
    updateLyrics({
      lines, sourceURL: sourcePreview.canonicalUrl, sourcePageId: sourcePreview.pageId,
      sourceRevisionId: sourcePreview.revisionId, sourceSha1: sourcePreview.sha1,
      sourceFetchedAt: sourcePreview.fetchedAt,
    });
    setSourcePreview(null);
    setCandidates([]);
  };

  const performerName = (id: number) => {
    const performer = performers.find((item) => item.performerId === id);
    return performer?.name["zh-CN"] || performer?.name["ja-JP"] || String(id);
  };

  return (
    <div className="lyrics-workspace">
      <aside className="lyrics-catalog">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索新曲或 musicId…" />
        <div className="lyrics-catalog-list">
          {catalog.map((item) => (
            <button key={item.musicId} className={selectedMusic?.musicId === item.musicId ? "active" : ""} onClick={() => chooseMusic(item)}>
              <strong>{item.title["zh-CN"] || item.title["ja-JP"]}</strong>
              <span>#{item.musicId} · {item.lyricsStatus === "published" ? "已发布" : item.lyricsStatus === "draft" ? "草稿" : "未录入"}</span>
            </button>
          ))}
        </div>
      </aside>

      <section className="lyrics-editor">
        {!selectedMusic ? <div className="center-state"><p>从目录选择一首新曲</p></div> : loading ? (
          <div className="center-state"><div className="spinner" />加载歌词…</div>
        ) : lyrics ? (
          <>
            <div className="lyrics-editor-head">
              <div><h2>{selectedMusic.title["zh-CN"] || selectedMusic.title["ja-JP"]}</h2><span>musicId {lyrics.musicId} · revision {lyrics.revision} · {lyrics.status === "published" ? "已发布" : "草稿"}</span></div>
              <div className="lyrics-actions">
                <button className="btn btn-secondary" onClick={findSource} disabled={busy || lyrics.revision > 0}>查找来源</button>
                <button className="btn btn-primary" onClick={save} disabled={busy || !dirty}>保存草稿</button>
                {role === "admin" && lyrics.revision > 0 && (
                  <button className="btn btn-secondary" onClick={() => publish(lyrics.status !== "published")} disabled={busy}>
                    {lyrics.status === "published" ? "取消发布" : "发布"}
                  </button>
                )}
              </div>
            </div>

            {error && (
              <div className="lyrics-error" role="alert">
                <strong>{sourceLabel(error)}</strong>
                {error.details.map((detail) => <span key={detail}>{detail}</span>)}
                {error.code === "revision_conflict" && error.current && (
                  <button className="btn btn-secondary btn-sm" onClick={() => {
                    setLyrics(error.current!); setBaseline(JSON.stringify(error.current)); setError(null);
                  }}>载入服务器版本</button>
                )}
              </div>
            )}

            {candidates.length > 0 && (
              <div className="lyrics-source-panel">
                <strong>候选来源</strong>
                {candidates.map((candidate) => (
                  <button key={`${candidate.pageId}-${candidate.revisionId}`} onClick={() => previewSource(candidate)} disabled={busy}>
                    {candidate.title}<span>revision {candidate.revisionId}</span>
                  </button>
                ))}
              </div>
            )}

            {sourcePreview && (
              <div className="lyrics-source-preview">
                <div><strong>来源预览 · {sourcePreview.lines.length} 行</strong><a href={sourcePreview.canonicalUrl} target="_blank" rel="noreferrer">打开来源</a></div>
                <pre>{sourcePreview.lines.slice(0, 12).map((line) => line.japanese).join("\n")}</pre>
                <div className="lyrics-actions"><button className="btn btn-primary" onClick={acceptPreview}>使用此版本</button><button className="btn btn-ghost" onClick={() => setSourcePreview(null)}>取消</button></div>
              </div>
            )}

            <div className="lyrics-metadata">
              <label>来源备注<input value={lyrics.sourceNote || ""} onChange={(event) => updateLyrics({ sourceNote: event.target.value })} /></label>
              <label>授权备注<input value={lyrics.licenseNote || ""} onChange={(event) => updateLyrics({ licenseNote: event.target.value })} /></label>
              {lyrics.sourceURL && <a href={lyrics.sourceURL} target="_blank" rel="noreferrer">已锁定来源 revision {lyrics.sourceRevisionId}</a>}
            </div>

            <div className="lyrics-lines">
              {lyrics.lines.map((line, lineIndex) => (
                <article className="lyric-line" key={line.id}>
                  <header><strong>{lineIndex + 1}</strong><code>{line.id}</code><label><input type="checkbox" checked={Boolean(line.stanzaBreakBefore)} onChange={(event) => updateLine(lineIndex, { stanzaBreakBefore: event.target.checked })} /> 段落前空行</label></header>
                  <div className="lyric-translations">
                    <label>日文<textarea value={line.japanese} readOnly rows={2} /></label>
                    <label>简中<textarea value={line["zh-CN"]} onChange={(event) => updateLine(lineIndex, { "zh-CN": event.target.value })} rows={2} /></label>
                    <label>English<textarea value={line["en-US"]} onChange={(event) => updateLine(lineIndex, { "en-US": event.target.value })} rows={2} /></label>
                  </div>
                  <div className="lyric-segments">
                    {line.segments.map((segment, segmentIndex) => (
                      <div key={`${line.id}-${segmentIndex}`}>
                        <input aria-label={`第 ${lineIndex + 1} 行分段 ${segmentIndex + 1}`} value={segment.text} readOnly={lyrics.revision > 0} onChange={(event) => updateSegment(lineIndex, segmentIndex, event.target.value)} />
                        <select multiple value={segment.performerIds.map(String)} onChange={(event) => updateSegment(lineIndex, segmentIndex, segment.text, Array.from(event.target.selectedOptions, (option) => Number(option.value)))}>
                          {performers.map((performer) => <option key={performer.performerId} value={performer.performerId}>{performer.name["zh-CN"] || performer.name["ja-JP"]}</option>)}
                        </select>
                        <span>{segment.performerIds.map(performerName).join(" / ") || "未指定演唱者"}</span>
                      </div>
                    ))}
                  </div>
                </article>
              ))}
              {lyrics.revision === 0 && <button className="btn btn-secondary lyrics-add-line" onClick={addLine}>添加歌词行</button>}
            </div>

            <div className="lyrics-public-preview">
              <div className="lyrics-preview-tabs">
                {(["ja-JP", "zh-CN", "en-US"] as const).map((locale) => <button key={locale} className={previewLocale === locale ? "active" : ""} onClick={() => setPreviewLocale(locale)}>{locale}</button>)}
              </div>
              {lyrics.lines.map((line) => <p key={line.id}>{previewLocale === "ja-JP" ? line.japanese : line[previewLocale]}</p>)}
            </div>
          </>
        ) : <div className="center-state"><p>歌词加载失败</p></div>}
      </section>
    </div>
  );
}
