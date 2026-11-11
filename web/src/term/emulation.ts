// Terminal "personalities": what we report to the host and which key
// sequences we generate.

export type EmulationId =
  | "xterm"
  | "vt100"
  | "vt102"
  | "vt220"
  | "vt320"
  | "ansi"
  | "linux"
  | "scoansi"
  | "wyse50"
  | "wyse60";

export type KeyFamily = "xterm" | "vt100" | "vt220" | "linux" | "sco" | "wyse";

export interface Emulation {
  id: EmulationId;
  label: string;
  /** default terminal type string sent to the host (TERM / TTYPE) */
  termType: string;
  /** primary / secondary device attributes replies (without CSI) */
  da1: string;
  da2: string;
  keys: KeyFamily;
  /** DEC conformance level (1 = VT100 ... 4 = VT420), 0 for non-DEC */
  level: number;
  /** control language spoken by the host */
  dialect: "ansi" | "wyse";
}

export const EMULATIONS: Emulation[] = [
  { id: "xterm", label: "Xterm", termType: "xterm-256color", da1: "?64;1;2;6;9;15;18;21;22c", da2: ">41;354;0c", keys: "xterm", level: 4, dialect: "ansi" },
  { id: "vt100", label: "VT100", termType: "vt100", da1: "?1;2c", da2: ">0;10;0c", keys: "vt100", level: 1, dialect: "ansi" },
  { id: "vt102", label: "VT102", termType: "vt102", da1: "?6c", da2: ">0;10;0c", keys: "vt100", level: 1, dialect: "ansi" },
  { id: "vt220", label: "VT220", termType: "vt220", da1: "?62;1;2;6;8;9;15c", da2: ">1;10;0c", keys: "vt220", level: 2, dialect: "ansi" },
  { id: "vt320", label: "VT320", termType: "vt320", da1: "?63;1;2;6;8;9;15c", da2: ">24;10;0c", keys: "vt220", level: 3, dialect: "ansi" },
  { id: "ansi", label: "ANSI", termType: "ansi", da1: "?1;2c", da2: ">0;10;0c", keys: "xterm", level: 1, dialect: "ansi" },
  { id: "linux", label: "Linux", termType: "linux", da1: "?6c", da2: ">0;10;0c", keys: "linux", level: 1, dialect: "ansi" },
  { id: "scoansi", label: "SCO ANSI", termType: "scoansi", da1: "?1;2c", da2: ">0;10;0c", keys: "sco", level: 1, dialect: "ansi" },
  { id: "wyse50", label: "Wyse 50", termType: "wyse50", da1: "", da2: "", keys: "wyse", level: 0, dialect: "wyse" },
  { id: "wyse60", label: "Wyse 60", termType: "wyse60", da1: "", da2: "", keys: "wyse", level: 0, dialect: "wyse" },
];

export function emulationById(id: string | undefined): Emulation {
  return EMULATIONS.find((e) => e.id === id) ?? EMULATIONS[0];
}
