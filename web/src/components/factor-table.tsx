"use client";

import type { ScoreFactor } from "@/lib/types";
import { factorLabel } from "@/lib/format";

/**
 * The reproducible factor breakdown behind a base score: input, weight and
 * what each factor contributed.
 */
export function FactorTable({
  factors,
  baseScore,
}: {
  factors: ScoreFactor[];
  baseScore: number;
}) {
  return (
    <table className="factors">
      <thead>
        <tr>
          <th scope="col">Factor</th>
          <th scope="col">Input</th>
          <th scope="col">Weight</th>
          <th scope="col">Contribution</th>
        </tr>
      </thead>
      <tbody>
        {factors.map((f) => (
          <tr key={f.key}>
            <td>{factorLabel(f.key)}</td>
            <td className="muted">{f.input}</td>
            <td>{f.weight}</td>
            <td className={f.contribution < 0 ? "neg" : f.contribution > 0 ? "pos" : ""}>
              {f.contribution > 0 ? "+" : ""}
              {f.contribution}
            </td>
          </tr>
        ))}
        <tr>
          <td colSpan={3} style={{ fontWeight: 600 }}>
            Base score
          </td>
          <td style={{ fontWeight: 600 }}>{baseScore}</td>
        </tr>
      </tbody>
    </table>
  );
}
