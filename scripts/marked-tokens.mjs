#!/usr/bin/env node
// Prints the token stream of marked 18.0.11 for the inputs given on stdin,
// one JSON document per input, so the Go port can be compared with it.
// When marked is not available, the script prints MARKED-NOT-FOUND on stderr
// and exits with status 3, so the differential test can skip instead of
// fail.
//
// Usage: node scripts/marked-tokens.mjs [marked-dir] < inputs.txt

import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
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
  console.error('MARKED-NOT-FOUND: pass the marked module directory as the first argument');
  process.exit(3);
}

const { Marked } = await loadMarked();

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

const inputs = readFileSync(0, 'utf8').split('\n---\n');
for (const input of inputs) {
  if (input === '') {
    continue;
  }
  const marked = new Marked({});
  const tokens = marked.lexer(input).map(canonicalToken);
  process.stdout.write(`${JSON.stringify(tokens)}\n`);
}
