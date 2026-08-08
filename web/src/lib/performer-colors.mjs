// Official Project SEKAI representative colours supplied by the product owner.
// Wiki legend colours are provenance only and must not override these display colours.
export const OFFICIAL_PERFORMER_COLORS = Object.freeze({
  miku: "#33CCBB",
  rin: "#FFCC11",
  len: "#FFEE11",
  luka: "#FFBBCC",
  meiko: "#DD4444",
  kaito: "#3366CC",
  ichika: "#33AAEE",
  saki: "#FFDD44",
  honami: "#EE6666",
  shiho: "#BBDD22",
  minori: "#FFCCAA",
  haruka: "#99CCFF",
  airi: "#FFAACC",
  shizuku: "#99EEDD",
  kohane: "#FF6699",
  an: "#00BBDD",
  akito: "#FF7722",
  toya: "#0077DD",
  tsukasa: "#FFBB00",
  emu: "#FF66BB",
  nene: "#33DD99",
  rui: "#BB88EE",
  kanade: "#BB6688",
  mafuyu: "#8888CC",
  ena: "#CCAA88",
  mizuki: "#DDAACC",
});

export const OFFICIAL_UNIT_COLORS = Object.freeze({
  vs: "#33CCBB",
  ln: "#4455DD",
  mmj: "#88DD44",
  vbs: "#EE1166",
  ws: "#FF9900",
  n25: "#885599",
});

const PERFORMER_ALIASES = Object.freeze({
  hatsunemiku: "miku", "初音ミク": "miku", 初音未来: "miku",
  kagaminerin: "rin", "鏡音リン": "rin", 镜音铃: "rin", 鏡音鈴: "rin",
  kagaminelen: "len", "鏡音レン": "len", 镜音连: "len", 鏡音連: "len",
  megurineluka: "luka", "巡音ルカ": "luka", 巡音流歌: "luka",
  星乃一歌: "ichika",
  天馬咲希: "saki", 天马咲希: "saki",
  望月穂波: "honami", 望月穗波: "honami",
  日野森志歩: "shiho", 日野森志步: "shiho",
  花里みのり: "minori", 花里实乃理: "minori",
  桐谷遥: "haruka", 桐谷遙: "haruka",
  桃井愛莉: "airi", 桃井爱莉: "airi",
  日野森雫: "shizuku",
  小豆沢こはね: "kohane", 小豆泽心羽: "kohane",
  白石杏: "an",
  東雲彰人: "akito", 东云彰人: "akito",
  青柳冬弥: "toya",
  天馬司: "tsukasa", 天马司: "tsukasa",
  鳳えむ: "emu", 凤笑梦: "emu",
  草薙寧々: "nene", 草薙宁宁: "nene",
  神代類: "rui", 神代类: "rui",
  宵崎奏: "kanade",
  朝比奈まふゆ: "mafuyu", 朝比奈真冬: "mafuyu",
  東雲絵名: "ena", 东云绘名: "ena",
  暁山瑞希: "mizuki", 晓山瑞希: "mizuki",
});

function normalizeColorKey(value) {
  return String(value || "").normalize("NFKC").trim().toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");
}

function canonicalPerformerKey(value) {
  const normalized = normalizeColorKey(value);
  return PERFORMER_ALIASES[normalized] || normalized;
}

export function unitRepresentativeColor(versionLabel = "") {
  const key = normalizeColorKey(versionLabel);
  if (key.includes("25ji") || key.includes("nightcord") || key.includes("25時") || key.includes("ナイトコード")) return OFFICIAL_UNIT_COLORS.n25;
  if (key.includes("leoneed")) return OFFICIAL_UNIT_COLORS.ln;
  if (key.includes("moremorejump")) return OFFICIAL_UNIT_COLORS.mmj;
  if (key.includes("vividbadsquad")) return OFFICIAL_UNIT_COLORS.vbs;
  if (key.includes("wonderlands") || key.includes("ワンダーランズ")) return OFFICIAL_UNIT_COLORS.ws;
  if (key.includes("virtualsinger") || key.includes("vocaloid")) return OFFICIAL_UNIT_COLORS.vs;
  return undefined;
}

export function performerRepresentativeColor(performerId, versionLabel = "", sourceColor) {
  const key = canonicalPerformerKey(performerId);
  const official = OFFICIAL_PERFORMER_COLORS[key];
  if (official) return official;
  if (["chorus", "ensemble", "all", "everyone"].includes(key)) {
    return unitRepresentativeColor(versionLabel) || sourceColor;
  }
  const unitAliases = {
    virtualsinger: "vs",
    leoneed: "ln",
    moremorejump: "mmj",
    vividbadsquad: "vbs",
    wonderlandsxshowtime: "ws",
    wonderlandsshowtime: "ws",
    n25: "n25",
    nightcord: "n25",
    "25ji": "n25",
  };
  const unit = unitAliases[key];
  return unit ? OFFICIAL_UNIT_COLORS[unit] : sourceColor;
}
