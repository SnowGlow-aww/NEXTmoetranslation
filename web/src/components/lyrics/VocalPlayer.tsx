"use client";

import { useEffect, useRef, useState, useCallback } from "react";
import {
  IMusicInfo,
  IMusicVocalInfo,
  getCharacterIconUrl,
  getCharacterLabel,
  getMusicVocalAudioUrl,
  getMusicVocalDetails,
} from "@/lib/music-vocals";

function formatTime(seconds: number): string {
  if (isNaN(seconds) || seconds < 0) return "0:00";
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, "0")}`;
}

interface SingleVocalPlayerProps {
  vocal: IMusicVocalInfo;
  fillerSec: number;
  outsideCharacters: Record<number, string>;
  activePlayingId: number | null;
  onPlayStateChange: (vocalId: number, isPlaying: boolean) => void;
}

function SingleVocalPlayer({
  vocal,
  fillerSec,
  outsideCharacters,
  activePlayingId,
  onPlayStateChange,
}: SingleVocalPlayerProps) {
  const [isPlaying, setIsPlaying] = useState(false);
  const [progress, setProgress] = useState(0);
  const [duration, setDuration] = useState(0);
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const audioUrl = getMusicVocalAudioUrl(vocal.assetbundleName);

  // If another track started playing, pause this one
  useEffect(() => {
    if (activePlayingId !== vocal.id && isPlaying) {
      if (audioRef.current) {
        audioRef.current.pause();
      }
      setIsPlaying(false);
    }
  }, [activePlayingId, vocal.id, isPlaying]);

  const togglePlay = useCallback(() => {
    if (!audioRef.current) {
      const audio = new Audio(audioUrl);
      audioRef.current = audio;

      audio.onended = () => {
        setIsPlaying(false);
        onPlayStateChange(vocal.id, false);
      };
      audio.onplay = () => {
        setIsPlaying(true);
        onPlayStateChange(vocal.id, true);
      };
      audio.onpause = () => {
        setIsPlaying(false);
        onPlayStateChange(vocal.id, false);
      };
      audio.onloadedmetadata = () => {
        if (audioRef.current) {
          setDuration(audioRef.current.duration);
        }
      };
      audio.ontimeupdate = () => {
        if (audioRef.current) {
          setProgress(audioRef.current.currentTime);
        }
      };

      // Skip initial silence offset
      if (fillerSec > 0) {
        audio.currentTime = fillerSec;
      }
    }

    if (isPlaying) {
      audioRef.current.pause();
    } else {
      audioRef.current.play().catch((err) => {
        console.warn("Audio playback prevented:", err);
      });
    }
  }, [audioUrl, fillerSec, isPlaying, onPlayStateChange, vocal.id]);

  const handleSeek = (e: React.ChangeEvent<HTMLInputElement>) => {
    const time = parseFloat(e.target.value);
    setProgress(time);
    if (audioRef.current) {
      audioRef.current.currentTime = time;
    }
  };

  // Cleanup audio on unmount
  useEffect(() => {
    return () => {
      if (audioRef.current) {
        audioRef.current.pause();
        audioRef.current = null;
      }
    };
  }, []);

  return (
    <div className="vocal-player-row">
      <div className="vocal-player-main">
        {/* Play/Pause Circle Button */}
        <button
          type="button"
          onClick={togglePlay}
          className={`vocal-play-btn ${isPlaying ? "playing" : ""}`}
          aria-label={isPlaying ? `暂停 ${vocal.caption}` : `播放 ${vocal.caption}`}
        >
          {isPlaying ? (
            <svg className="vocal-icon" fill="currentColor" viewBox="0 0 24 24">
              <path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z" />
            </svg>
          ) : (
            <svg className="vocal-icon play-offset" fill="currentColor" viewBox="0 0 24 24">
              <path d="M8 5v14l11-7z" />
            </svg>
          )}
        </button>

        <div className="vocal-player-info">
          {/* Header Row: Title & Download */}
          <div className="vocal-title-row">
            <span className="vocal-caption" title={vocal.caption}>
              {vocal.caption}
            </span>
            <a
              href={audioUrl}
              download={`${vocal.caption}.mp3`}
              target="_blank"
              rel="noopener noreferrer"
              className="vocal-download-btn"
              title="下载音频"
              onClick={(e) => e.stopPropagation()}
            >
              <svg className="vocal-download-icon" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"
                />
              </svg>
            </a>
          </div>

          {/* Character Avatars Row */}
          {vocal.characters && vocal.characters.length > 0 && (
            <div className="vocal-characters-row">
              {vocal.characters.map((chara) => {
                const isGameChar = chara.characterType === "game_character";
                const charName = getCharacterLabel(chara, outsideCharacters);
                const hasIcon = isGameChar && chara.characterId <= 26;

                return hasIcon ? (
                  <div
                    key={chara.id}
                    className="vocal-chara-avatar"
                    title={charName}
                  >
                    {/* eslint-disable-next-line @next/next/no-img-element */}
                    <img
                      src={getCharacterIconUrl(chara.characterId)}
                      alt={charName}
                      className="vocal-chara-img"
                    />
                  </div>
                ) : (
                  <div
                    key={chara.id}
                    className="vocal-chara-badge"
                    title={charName}
                  >
                    <span>{charName}</span>
                  </div>
                );
              })}
            </div>
          )}

          {/* Progress Slider & Timecode */}
          <div className="vocal-scrubber-row">
            <input
              type="range"
              min="0"
              max={duration || 100}
              step="0.1"
              value={progress}
              onChange={handleSeek}
              className="vocal-range-slider"
              aria-label="音频播放进度"
            />
            <span className="vocal-timecode font-mono">
              {formatTime(progress)} / {formatTime(duration)}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

export interface LyricsVocalCardProps {
  musicId: number;
}

export function LyricsVocalCard({ musicId }: LyricsVocalCardProps) {
  const [music, setMusic] = useState<IMusicInfo | null>(null);
  const [vocals, setVocals] = useState<IMusicVocalInfo[]>([]);
  const [outsideChars, setOutsideChars] = useState<Record<number, string>>({});
  const [loading, setLoading] = useState(true);
  const [activePlayingId, setActivePlayingId] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setActivePlayingId(null);

    getMusicVocalDetails(musicId)
      .then(({ music: m, vocals: v, outsideChars: oc }) => {
        if (!cancelled) {
          setMusic(m);
          setVocals(v);
          setOutsideChars(oc);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          console.warn("Failed to load vocals for music", musicId, err);
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [musicId]);

  const handlePlayStateChange = useCallback((vocalId: number, isPlaying: boolean) => {
    setActivePlayingId(isPlaying ? vocalId : null);
  }, []);

  if (loading) {
    return (
      <div className="lyrics-vocal-card loading" role="status" aria-live="polite">
        <div className="spinner" />
        <span>正在载入演唱版本音频…</span>
      </div>
    );
  }

  if (vocals.length === 0) {
    return null;
  }

  const fillerSec = music?.fillerSec && music.fillerSec > 0 ? Number(music.fillerSec.toFixed(1)) : 0;

  return (
    <div className="lyrics-vocal-card" aria-labelledby="lyrics-vocal-card-title">
      <div className="lyrics-vocal-header">
        <h3 id="lyrics-vocal-card-title" className="lyrics-vocal-title">
          <svg className="vocal-header-icon" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z"
            />
          </svg>
          {fillerSec > 0 ? `演唱版本（已跳过${fillerSec}秒空白）` : "演唱版本"}
        </h3>
        <span className="lyrics-vocal-count">共 {vocals.length} 个版本</span>
      </div>
      <div className="lyrics-vocal-list">
        {vocals.map((vocal) => (
          <SingleVocalPlayer
            key={vocal.id}
            vocal={vocal}
            fillerSec={music?.fillerSec || 0}
            outsideCharacters={outsideChars}
            activePlayingId={activePlayingId}
            onPlayStateChange={handlePlayStateChange}
          />
        ))}
      </div>
    </div>
  );
}
