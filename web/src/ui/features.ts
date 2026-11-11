// Extension points: feature modules (dialogs, panels) register themselves here
// so menus and shortcuts can invoke them without import cycles.
import { ComponentType } from "react";
import { Tab, toast } from "../state/store";

export const features: Record<string, (arg?: any) => void> = {};

export function feature(name: string, arg?: any): void {
  const f = features[name];
  if (f) f(arg);
  else toast(`"${name}" is not available in this build.`);
}

/** Views for non-terminal tab kinds (e.g. the SFTP browser). */
export const tabViews: Record<string, ComponentType<{ tab: Tab; active: boolean }>> = {};
