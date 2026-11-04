#!/usr/bin/env node
// Generates the token reference of the marked port.
//
// The script reads marked's own test corpus (the CommonMark and GFM specs and
// marked's regression fixtures) and writes the token stream that marked
// 18.0.11 produces for every case. The Go test in
// internal/markdown/marked/parity_test.go compares the port against this
// reference.
//
// The options of every case follow marked's own spec runner
// (test/run-spec-tests.js and @markedjs/testutils):
//
//   - The per-directory options are the default options of the directory.
//   - A spec file may override them with a front matter block.
//   - The options are handed to a Marked instance, so marked's own defaults
//     fill in everything the case does not mention.
//
// Usage: node scripts/marked-token-reference.mjs [marked-module-directory]
//
// The marked module is resolved from the directory given on the command line,
// or from node_modules of this repository, or from the marked checkout in
// /tmp/marked-18.0.11.

import { createRequire } from 'node:module';
import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
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

const { Marked } = await loadMarked();

const specRoot = join(repoRoot, 'internal/markdown/marked/testdata/specs');

const modes = [
  { dir: 'commonmark', options: { gfm: false, pedantic: false } },
  { dir: 'gfm', options: { gfm: true, pedantic: false } },
  { dir: 'new', options: {} },
  { dir: 'original', options: { gfm: false, pedantic: true } },
  { dir: 'redos', options: { silent: false } },
];

// parseFrontMatter splits the front matter of a spec file from its body.
// The front matter holds the options of the case, in the same simple
// "key: value" form that marked's own loader accepts.
function parseFrontMatter(source) {
  if (!source.startsWith('---')) {
    return { attributes: {}, body: source };
  }
  const end = source.indexOf('\n---', 3);
  if (end < 0) {
    return { attributes: {}, body: source };
  }
  const attributes = {};
  for (const line of source.slice(4, end).split('\n')) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) {
      continue;
    }
    const separator = trimmed.indexOf(':');
    if (separator < 0) {
      continue;
    }
    const key = trimmed.slice(0, separator).trim();
    let value = trimmed.slice(separator + 1).trim();
    if (value === 'true') {
      value = true;
    } else if (value === 'false') {
      value = false;
    } else if (/^-?\d+$/.test(value)) {
      value = Number(value);
    } else {
      value = value.replace(/^['"]|['"]$/g, '');
    }
    attributes[key] = value;
  }
  const body = source.slice(end + 4).replace(/^\n/, '');
  return { attributes, body };
}

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

// lexTokens lexes a case the way marked's spec runner does. The options of
// the case are given to a Marked instance, so marked's own defaults fill in
// everything the case does not mention.
function lexTokens(markdown, options) {
  const marked = new Marked(options);
  return marked.lexer(markdown).map(canonicalToken);
}

// renderHTML renders a case the way marked's spec runner does.
function renderHTML(markdown, options) {
  const marked = new Marked(options);
  return marked.parse(markdown);
}

const cases = [];

for (const mode of modes) {
  const dir = join(specRoot, mode.dir);
  for (const entry of readdirSync(dir).sort()) {
    const path = join(dir, entry);
    if (entry.endsWith('.json')) {
      const specs = JSON.parse(readFileSync(path, 'utf8'));
      specs.forEach((spec, index) => {
        const options = { ...mode.options, ...(spec.options ?? {}) };
        cases.push({
          name: `${mode.dir}/${entry}#${index + 1}`,
          markdown: spec.markdown,
          options,
          tokens: lexTokens(spec.markdown, options),
          html: renderHTML(spec.markdown, options),
        });
      });
    } else if (entry.endsWith('.md')) {
      const { attributes, body } = parseFrontMatter(readFileSync(path, 'utf8'));
      const options = { ...mode.options, ...attributes };
      cases.push({
        name: `${mode.dir}/${entry}`,
        markdown: body,
        options,
        tokens: lexTokens(body, options),
        html: renderHTML(body, options),
      });
    }
  }
}

const outFile = join(repoRoot, 'internal/markdown/marked/testdata/tokens.json');
writeFileSync(outFile, `${JSON.stringify(cases, null, 1)}\n`);
console.log(`wrote ${cases.length} cases to ${outFile}`);
