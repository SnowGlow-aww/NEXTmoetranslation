export interface CatalogPage<T> {
  items: T[];
  nextCursor?: string;
}

export function collectCatalogPages<T>(
  loadPage: (cursor: string) => Promise<CatalogPage<T>>,
): Promise<T[]>;
