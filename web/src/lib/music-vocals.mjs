export const GAME_CHARACTER_NAMES = Object.freeze({
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
  11: "東云 彰人",
  12: "青柳 冬弥",
  13: "天馬 司",
  14: "鳳 えむ",
  15: "草薙 寧々",
  16: "神代 類",
  17: "宵崎 奏",
  18: "朝比奈 まふゆ",
  19: "東云 絵名",
  20: "暁山 瑞希",
  21: "初音ミク",
  22: "鏡音リン",
  23: "鏡音レン",
  24: "巡音ルカ",
  25: "MEIKO",
  26: "KAITO",
});

export function getMusicVocalAudioUrl(assetbundleName) {
  return `https://storage.exmeaning.com/sekai-jp-assets/music/long/${assetbundleName}/${assetbundleName}.mp3`;
}

export function getCharacterIconUrl(characterId) {
  return `https://moe.exmeaning.com/assets/chr_ts_${characterId}.png`;
}

export function getCharacterLabel(character, outsideCharacters = {}) {
  if (character.characterType === "game_character") {
    return GAME_CHARACTER_NAMES[character.characterId] || `角色 ${character.characterId}`;
  }
  return outsideCharacters[character.characterId] || `嘉宾 ${character.characterId}`;
}

let cachedMusics = null;
let cachedVocals = null;
let cachedOutsideChars = null;
let fetchPromise = null;

const MASTER_PRIMARY = "https://metadata.exmeaning.com/jp/master";
const MASTER_FALLBACK = "https://metadata.pjsk.moe/jp/master";

async function fetchJsonWithFallback(filename) {
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

export async function loadMusicMasterData() {
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
        fetchJsonWithFallback("musics.json").catch(() => []),
        fetchJsonWithFallback("musicVocals.json").catch(() => []),
        fetchJsonWithFallback("outsideCharacters.json").catch(() => []),
      ]);

      const outsideMap = {};
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

export async function getMusicVocalDetails(musicId) {
  const { musics, vocals, outsideChars } = await loadMusicMasterData();
  const music = musics.find((m) => m.id === musicId) || null;
  const musicVocals = vocals.filter((v) => v.musicId === musicId);
  return {
    music,
    vocals: musicVocals,
    outsideChars,
  };
}
