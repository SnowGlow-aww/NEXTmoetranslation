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

export const GAME_CHARACTER_NAMES: Readonly<Record<number, string>>;

export function getMusicVocalAudioUrl(assetbundleName: string): string;

export function getCharacterIconUrl(characterId: number): string;

export function getCharacterLabel(
  character: IMusicVocalCharacter,
  outsideCharacters?: Record<number, string>
): string;

export function loadMusicMasterData(): Promise<{
  musics: IMusicInfo[];
  vocals: IMusicVocalInfo[];
  outsideChars: Record<number, string>;
}>;

export function getMusicVocalDetails(musicId: number): Promise<{
  music: IMusicInfo | null;
  vocals: IMusicVocalInfo[];
  outsideChars: Record<number, string>;
}>;
