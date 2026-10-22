const slugify = (s: string): string => {
  const base = s
    .toLowerCase()
    .normalize("NFKD")
    // strip accents after decomposition
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
  return base;
};

export { slugify };
