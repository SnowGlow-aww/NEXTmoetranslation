export interface IMusicInfo {
  id: number;
  title: string;
  fillerSec?: number;
  assetbundleName?: string;
}

export interface IMusicVocalCharacter {
  id: number;
  musicId: number;
  musicVocalId: number;
  characterType: "game_character" | "outside_character" | string;
  characterId: number;
  seq: number;
}

export interface IMusicVocalInfo {
  id: number;
  musicId: number;
  musicVocalType?: string;
  caption: string;
  characters?: IMusicVocalCharacter[];
  assetbundleName: string;
  archivePublishedAt?: number;
}

export interface IOutsideCharacter {
  id: number;
  name: string;
}

export const GAME_CHARACTER_NAMES: Record<number, string> = {
  1: "星乃 一歌",
  2: "天馬 咲希",
  3: "望月 穂波",
  4: "日野森 志歩",
  5: "花里 みのり",
  6: "桐谷 遥",
  7: "桃井 愛莉",
  8: "日野森 雫",
  9: "小豆沢 こはね",
  10: "白石 杏",
  11: "東雲 彰人",
  12: "青柳 冬弥",
  13: "天馬 司",
  14: "鳳 えむ",
  15: "草薙 寧々",
  16: "神代 類",
  17: "宵崎 奏",
  18: "朝比奈 まふゆ",
  19: "東雲 絵名",
  20: "暁山 瑞希",
  21: "初音ミク",
  22: "鏡音リン",
  23: "鏡音レン",
  24: "巡音ルカ",
  25: "MEIKO",
  26: "KAITO",
};

export function getMusicVocalAudioUrl(assetbundleName: string): string {
  return `https://storage.exmeaning.com/sekai-jp-assets/music/long/${assetbundleName}/${assetbundleName}.mp3`;
}

export function getCharacterIconUrl(characterId: number): string {
  return `https://moe.exmeaning.com/assets/chr_ts_${characterId}.png`;
}

export function getCharacterLabel(
  character: IMusicVocalCharacter,
  outsideCharacters: Record<number, string>
): string {
  if (character.characterType === "game_character") {
    return GAME_CHARACTER_NAMES[character.characterId] || `角色 ${character.characterId}`;
  }
  return outsideCharacters[character.characterId] || `嘉宾 ${character.characterId}`;
}

// In-memory caches for masterdata
let cachedMusics: IMusicInfo[] | null = null;
let cachedVocals: IMusicVocalInfo[] | null = null;
let cachedOutsideChars: Record<number, string> | null = null;
let fetchPromise: Promise<{
  musics: IMusicInfo[];
  vocals: IMusicVocalInfo[];
  outsideChars: Record<number, string>;
}> | null = null;

const MASTER_PRIMARY = "https://metadata.exmeaning.com/jp/master";
const MASTER_FALLBACK = "https://metadata.pjsk.moe/jp/master";

async function fetchJsonWithFallback<T>(filename: string): Promise<T> {
  try {
    const res = await fetch(`${MASTER_PRIMARY}/${filename}`);
    if (res.ok) return await res.json();
  } catch {
    // Fallback to secondary mirror
  }
  const fallbackRes = await fetch(`${MASTER_FALLBACK}/${filename}`);
  if (!fallbackRes.ok) {
    throw new Error(`Failed to fetch ${filename}`);
  }
  return await fallbackRes.json();
}

export async function loadMusicMasterData(): Promise<{
  musics: IMusicInfo[];
  vocals: IMusicVocalInfo[];
  outsideChars: Record<number, string>;
}> {
  if (cachedMusics && cachedVocals && cachedOutsideChars) {
    return {
      musics: cachedMusics,
      vocals: cachedVocals,
      outsideChars: cachedOutsideChars,
    };
  }

  if (fetchPromise) {
    return fetchPromise;
  }

  fetchPromise = (async () => {
    try {
      const [musicsData, vocalsData, outsideData] = await Promise.all([
        fetchJsonWithFallback<IMusicInfo[]>("musics.json").catch(() => [] as IMusicInfo[]),
        fetchJsonWithFallback<IMusicVocalInfo[]>("musicVocals.json").catch(() => [] as IMusicVocalInfo[]),
        fetchJsonWithFallback<IOutsideCharacter[]>("outsideCharacters.json").catch(() => [] as IOutsideCharacter[]),
      ]);

      const outsideMap: Record<number, string> = {};
      for (const item of outsideData) {
        outsideMap[item.id] = item.name;
      }

      cachedMusics = musicsData;
      cachedVocals = vocalsData;
      cachedOutsideChars = outsideMap;

      return {
        musics: musicsData,
        vocals: vocalsData,
        outsideChars: outsideMap,
      };
    } finally {
      fetchPromise = null;
    }
  })();

  return fetchPromise;
}

export async function getMusicVocalDetails(musicId: number): Promise<{
  music: IMusicInfo | null;
  vocals: IMusicVocalInfo[];
  outsideChars: Record<number, string>;
}> {
  const { musics, vocals, outsideChars } = await loadMusicMasterData();
  const music = musics.find((m) => m.id === musicId) || null;
  const musicVocals = vocals.filter((v) => v.musicId === musicId);
  return {
    music,
    vocals: musicVocals,
    outsideChars,
  };
}
