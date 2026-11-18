/*
 * MIT License
 *
 * Copyright (c) 2026 DE TECHNOLOGY LIMITED
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

// The build-free demo inlines SVG paths. Compare them with the Lucide version used by the admin app.
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import {
  Bug,
  Check,
  ChevronDown,
  CircleCheck,
  Lightbulb,
  MessageCircle,
  Paperclip,
  RotateCcw,
  Star,
  X,
  type LucideIcon,
} from "lucide-react";

const DEMO = fileURLToPath(new URL("../../../demo/index.html", import.meta.url));

/** Every Lucide glyph the demo page is allowed to inline. */
const ALLOWED: Record<string, LucideIcon> = {
  Bug,
  Check,
  ChevronDown,
  CircleCheck,
  Lightbulb,
  MessageCircle,
  Paperclip,
  RotateCcw,
  Star,
  X,
};

/**
 * Reduces SVG markup to an ordered list of "tag:geometry" pairs, so the
 * comparison survives formatting differences (self-closing slashes, attribute
 * order, the wrapper's own width/height/class).
 */
function geometry(svgMarkup: string): string {
  const parts: string[] = [];
  const re = /<(path|polygon|circle|rect|line|polyline)\b([^>]*)>/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(svgMarkup))) {
    const [, tag, attrs] = m;
    const keep = [...attrs.matchAll(/\b(d|points|cx|cy|r|x|y|width|height|x1|y1|x2|y2)="([^"]*)"/g)]
      .map(([, k, v]) => `${k}=${v.replace(/\s+/g, " ").trim()}`)
      .sort()
      .join(",");
    parts.push(`${tag}:${keep}`);
  }
  return parts.join("|");
}

/** The official geometry for each allowed icon, keyed by that geometry. */
const official = new Map<string, string>();
for (const [name, Cmp] of Object.entries(ALLOWED)) {
  const g = geometry(renderToStaticMarkup(createElement(Cmp)));
  assert.ok(g, `could not read geometry for ${name}`);
  official.set(g, name);
}

/** Every inline SVG body in the demo: both lucideSvg('…') calls and markup. */
function demoIcons(html: string): Array<{ where: string; geom: string }> {
  const found: Array<{ where: string; geom: string }> = [];
  for (const m of html.matchAll(/lucideSvg\(\s*'([^']*)'/g)) {
    found.push({ where: `lucideSvg('${m[1].slice(0, 40)}…')`, geom: geometry(m[1]) });
  }
  for (const m of html.matchAll(/<svg\b[^>]*>([\s\S]*?)<\/svg>/g)) {
    found.push({ where: `<svg> in markup: ${m[1].slice(0, 40).replace(/\s+/g, " ")}…`, geom: geometry(m[1]) });
  }
  // TYPE_ICONS entries are plain strings, not lucideSvg() call arguments.
  for (const m of html.matchAll(/^\s*(bug|suggestion|other):\s*'(<path[^']*)'/gm)) {
    found.push({ where: `TYPE_ICONS.${m[1]}`, geom: geometry(m[2]) });
  }
  return found.filter((f) => f.geom);
}

test("every inline icon in the demo page matches the Lucide original", () => {
  const html = readFileSync(DEMO, "utf8");
  const icons = demoIcons(html);

  assert.ok(icons.length >= 8, `expected to find the demo's inline icons, found ${icons.length}`);

  const unknown = icons.filter((i) => !official.has(i.geom));
  assert.deepEqual(
    unknown.map((u) => u.where),
    [],
    "these inline SVGs do not match any allowed Lucide icon at the installed version — " +
      "either Lucide redrew the glyph (copy the new geometry across) or someone hand-drew one " +
      "(use a Lucide icon instead, and add it to ALLOWED)",
  );
});

test("the demo renders every feedback control icon", () => {
  const html = readFileSync(DEMO, "utf8");
  const used = new Set(demoIcons(html).map((i) => official.get(i.geom)!));
  for (const name of ["Bug", "Lightbulb", "MessageCircle", "ChevronDown", "Check", "Star", "X", "RotateCcw", "Paperclip"]) {
    assert.ok(used.has(name), `the demo no longer renders ${name}`);
  }
});
