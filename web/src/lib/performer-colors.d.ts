export const OFFICIAL_PERFORMER_COLORS: Readonly<Record<string, string>>;
export const OFFICIAL_UNIT_COLORS: Readonly<Record<string, string>>;
export function unitRepresentativeColor(versionLabel?: string): string | undefined;
export function performerRepresentativeColor(performerId: string, versionLabel?: string, sourceColor?: string): string | undefined;
