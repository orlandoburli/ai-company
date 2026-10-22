import type { SVGProps } from "react";

const base = (p: SVGProps<SVGSVGElement>): SVGProps<SVGSVGElement> => ({
  width: 14,
  height: 14,
  viewBox: "0 0 16 16",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.6,
  strokeLinecap: "round",
  strokeLinejoin: "round",
  "aria-hidden": true,
  ...p,
});

/* Every icon needs caller props (size/className), so the parameter stays. */
function svgProps(p: SVGProps<SVGSVGElement>): SVGProps<SVGSVGElement> {
  return base(p ?? {});
}

export const IconChevron = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M6 3.5 10.5 8 6 12.5" />
  </svg>
);

export const IconPin = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M9.5 1.8 14.2 6.5 12 6.8 9.8 9 10 12.2 8 12 5.4 9.4 1.9 12.2 3.4 7.9 1.6 4.7 4 6 5 3.4z" fill="currentColor" stroke="none" />
  </svg>
);

export const IconPause = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M5.5 3v10M10.5 3v10" />
  </svg>
);

export const IconBlocked = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <circle cx="8" cy="8" r="6" />
    <path d="M4 4.8 11.2 12" />
  </svg>
);

export const IconEpic = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M3 13V6l3-2.5V8l3-3.5L12 8V3h2v10z" />
  </svg>
);

export const IconCheck = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="m3.5 8.5 3 3 6-7" />
  </svg>
);

export const IconPlus = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M8 3v10M3 8h10" />
  </svg>
);

export const IconClose = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="m4 4 8 8M12 4l-8 8" />
  </svg>
);

export const IconMenu = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" />
  </svg>
);

export const IconLogout = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M6 14H2V2h4M10 4l4 4-4 4M14 8H6" />
  </svg>
);

export const IconTrash = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <path d="M3 4.5h10M6.5 4.5v-2h3v2M4.5 4.5 5 14h6l.5-9.5M6.8 7v4M9.2 7v4" />
  </svg>
);

export const IconCopy = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
    <path d="M10.5 5.5v-2a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" />
  </svg>
);

export const IconUser = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <circle cx="8" cy="5.5" r="2.8" />
    <path d="M2.5 13.8c.8-2.4 2.9-3.8 5.5-3.8s4.7 1.4 5.5 3.8" />
  </svg>
);

export const IconBuilding = (p: SVGProps<SVGSVGElement>) => (
  <svg {...svgProps(p)}>
    <rect x="2.5" y="2.5" width="11" height="11" rx="1.5" />
    <path d="M5.5 5.5h1M9.5 5.5h1M5.5 8.5h1M9.5 8.5h1M6.5 13.5v-2h3v2" />
  </svg>
);

export const StatusDot = ({ color, title }: { color: string; title: string }) => (
  <span
    title={title}
    style={{
      width: 8,
      height: 8,
      borderRadius: 999,
      background: color,
      flex: "none",
      display: "inline-block",
    }}
  />
);
