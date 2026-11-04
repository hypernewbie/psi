#!/usr/bin/env node
// Generates the reference of the CJK friendly tokenizer.
//
// The script renders a set of CJK markdown cases with marked 18.0.11 and
// marked-cjk-friendly 0.1.2, the way pi uses them, and writes the token
// stream to internal/markdown/marked/testdata/cjk.json. The Go test in
// internal/markdown/marked/cjkfriendly_test.go compares the port against this
// reference.
//
// Usage: node scripts/marked-cjk-reference.mjs [marked-module-directory]
//
// The marked module is resolved from the directory given on the command line,
// or from node_modules of this repository, or from the marked checkout in
// /tmp/marked-ref.

import { createRequire } from 'node:module';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..');
const require = createRequire(import.meta.url);

function loadMarked() {
  const candidates = [
    process.argv[2],
    join(repoRoot, 'node_modules', 'marked'),
    '/tmp/marked-ref/node_modules/marked',
    '/tmp/marked-18.0.11',
  ].filter(Boolean);
  for (const candidate of candidates) {
    try {
      const entry = require.resolve(join(candidate, 'lib/marked.esm.js'));
      return import(entry);
    } catch {
      // try the next candidate
    }
  }
  throw new Error('marked 18.0.11 not found; pass the module directory as the first argument');
}

function loadCjkFriendly() {
  const candidates = [
    process.argv[3],
    join(repoRoot, 'node_modules', 'marked-cjk-friendly'),
    '/tmp/cjkre/package',
  ].filter(Boolean);
  for (const candidate of candidates) {
    try {
      const entry = require.resolve(join(candidate, 'dist/index.js'));
      return import(entry);
    } catch {
      // try the next candidate
    }
  }
  throw new Error('marked-cjk-friendly 0.1.2 not found; pass the module directory as the third argument');
}

const { Marked } = await loadMarked();
const { default: markedCjkFriendly } = await loadCjkFriendly();

// The cases cover the CJK friendly flanking rules of the extension.
const cases = [
  '**粗体**中文',
  '中文**粗体**',
  '**粗体**。',
  '。**粗体**中文',
  '*斜体*中文',
  '中文*斜体*',
  '中文。*斜体*。',
  '~~删除~~中文',
  '中文~~删除~~',
  '**粗体**中文**粗体**',
  '中文**粗体**中文',
  '**a**中**b**',
  '中**a**文**b**字',
  '_下划线_中文',
  '中文_下划线_',
  '**粗体**和*斜体*以及~~删除~~',
  '中文**粗体**、中文*斜体*、中文~~删除~~',
  '**粗**体**',
  '中文**粗**体**',
  '**粗体**',
  '中文**粗体**中文**斜体**中文',
  'a**粗体**b',
  '中文**粗体**。',
  '。**粗体**。',
  '（**粗体**）',
  '中文（**粗体**）中文',
  '~~删除~~。',
  '。~~删除~~',
  '**粗体** ',
  ' **粗体**',
  '中文 **粗体** 中文',
  '**粗体**中文*斜体*中文~~删除~~中文',
];

// canonicalToken mirrors the shape that the Go port writes.
function canonicalToken(token) {
  const out = { type: token.type, raw: token.raw };
  switch (token.type) {
    case 'code':
      out.text = token.text;
      out.lang = token.lang ?? '';
      out.codeBlockStyle = token.codeBlockStyle ?? '';
      break;
    case 'heading':
      out.text = token.text;
      out.depth = token.depth;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case 'blockquote':
      out.text = token.text;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case 'list':
      out.ordered = token.ordered;
      out.start = token.start;
      out.loose = token.loose;
      out.items = token.items.map((item) => ({
        raw: item.raw,
        task: item.task,
        checked: item.checked ?? false,
        loose: item.loose,
        text: item.text,
        tokens: item.tokens.map(canonicalToken),
      }));
      break;
    case 'html':
      out.text = token.text;
      out.pre = token.pre;
      out.block = token.block;
      out.inLink = token.inLink ?? false;
      out.inRawBlock = token.inRawBlock ?? false;
      break;
    case 'def':
      out.tag = token.tag;
      out.href = token.href;
      out.title = token.title ?? '';
      break;
    case 'table':
      out.align = token.align;
      out.header = token.header.map(canonicalCell);
      out.rows = token.rows.map((row) => row.map(canonicalCell));
      break;
    case 'paragraph':
    case 'text':
      out.text = token.text;
      out.tokens = (token.tokens ?? []).map(canonicalToken);
      break;
    case 'escape':
      out.text = token.text;
      break;
    case 'link':
    case 'image':
      out.text = token.text;
      out.href = token.href;
      out.title = token.title ?? '';
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case 'strong':
    case 'em':
    case 'del':
      out.text = token.text;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case 'codespan':
      out.text = token.text;
      break;
    case 'checkbox':
      out.checked = token.checked;
      break;
    default:
      break;
  }
  return out;
}

function canonicalCell(cell) {
  return {
    text: cell.text,
    header: cell.header,
    align: cell.align ?? null,
    tokens: (cell.tokens ?? []).map(canonicalToken),
  };
}

const out = [];
for (const markdown of cases) {
  const marked = new Marked();
  marked.use({
    tokenizer: { emStrong: markedCjkFriendly().tokenizer?.emStrong },
  });
  const tokens = marked.lexer(markdown).map(canonicalToken);
  out.push({ markdown, tokens });
}

const outFile = join(repoRoot, 'internal/markdown/marked/testdata/cjk.json');
writeFileSync(outFile, `${JSON.stringify(out, null, 1)}\n`);
console.log(`wrote ${out.length} cases to ${outFile}`);
