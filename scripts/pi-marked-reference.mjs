#!/usr/bin/env node
// Generates the token reference of pi's markdown stack.
//
// pi runs marked 18.0.11 with marked-cjk-friendly's emStrong tokenizer, a
// strict strikethrough tokenizer, and latex extensions for the block and the
// inline level. The script wires the same extensions — copied verbatim from
// packages/tui/src/components/markdown.ts of the pi checkout — and writes the
// token stream to internal/markdown/marked/testdata/pi.json. The Go test in
// internal/markdown/marked/piextensions_test.go compares the port against
// this reference.
//
// Usage: node scripts/pi-marked-reference.mjs [marked-dir] [cjk-dir]
//
// The marked module is resolved from the directory given on the command line,
// or from node_modules of this repository, or from the marked checkout in
// /tmp/marked-ref. marked-cjk-friendly comes from the package extracted into
// /tmp/cjkre/package by default.

import { createRequire } from 'node:module';
import { writeFileSync } from 'node:fs';
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

const { Marked, Tokenizer } = await loadMarked();
const { default: markedCjkFriendly } = await loadCjkFriendly();

// The extensions below are copied from
// temp/pi-mono/packages/tui/src/components/markdown.ts.

const STRICT_STRIKETHROUGH_REGEX = /^(~~)(?=[^\s~])((?:\\.|[^\\])*?(?:\\.|[^\s~\\]))\1(?=[^~]|$)/;

class StrictStrikethroughTokenizer extends Tokenizer {
  del(src) {
    const match = STRICT_STRIKETHROUGH_REGEX.exec(src);
    if (!match) {
      return undefined;
    }

    const text = match[2];
    return {
      type: "del",
      raw: match[0],
      text,
      tokens: this.lexer.inlineTokens(text),
    };
  }
}

function isEscaped(source, index) {
  let backslashes = 0;
  for (let position = index - 1; position >= 0 && source[position] === "\\"; position--) {
    backslashes++;
  }
  return backslashes % 2 === 1;
}

function findClosingDelimiter(source, closing, start) {
  let index = source.indexOf(closing, start);
  while (index >= 0 && isEscaped(source, index)) {
    index = source.indexOf(closing, index + closing.length);
  }
  return index;
}

function looksLikePendingDollarMath(source) {
  return /\\[A-Za-z]+|[_^=+*/<>()[\]|±≤≥≠≈∈→⇒∞∫∑√-]/.test(source);
}

function tokenizeInlineLatex(source) {
  let opening = "";
  let closing = "";
  if (source.startsWith("$$")) {
    opening = "$$";
    closing = "$$";
  } else if (source.startsWith("\\(")) {
    opening = "\\(";
    closing = "\\)";
  } else if (source.startsWith("\\[")) {
    opening = "\\[";
    closing = "\\]";
  } else if (source.startsWith("$") && !/^\$\s/.test(source)) {
    opening = "$";
    closing = "$";
  } else {
    return undefined;
  }

  const closingIndex = findClosingDelimiter(source, closing, opening.length);
  if (
    closingIndex >= 0 &&
    opening === "$" &&
    (/\s$/.test(source.slice(opening.length, closingIndex)) ||
      /^\d/.test(source.slice(closingIndex + 1)) ||
      (/^[A-Z_][A-Z0-9_]*(?:[^A-Za-z0-9_\s])?$/.test(source.slice(opening.length, closingIndex)) &&
        /^[A-Za-z_][A-Za-z0-9_]*/.test(source.slice(closingIndex + 1))) ||
      source.slice(opening.length, closingIndex).includes("`"))
  ) {
    return undefined;
  }

  if (closingIndex < 0) {
    const pendingSource = source.slice(opening.length);
    if (opening.startsWith("\\") || looksLikePendingDollarMath(pendingSource)) {
      return { type: "latex", raw: source, text: pendingSource, pending: true };
    }
    return undefined;
  }

  const text = source.slice(opening.length, closingIndex);
  if (!text || text.includes("\n")) {
    return undefined;
  }

  const raw = source.slice(0, closingIndex + closing.length);
  return { type: "latex", raw, text };
}

const TABLE_DELIMITER_ROW_REGEX = /^ {0,3}\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;

function containsTableStart(source) {
  const lines = source.split("\n");
  for (let index = 0; index < lines.length - 1; index++) {
    const delimiterRow = lines[index + 1];
    if ((delimiterRow.includes("|") || lines[index].includes("|")) && TABLE_DELIMITER_ROW_REGEX.test(delimiterRow)) {
      return true;
    }
  }
  return false;
}

function tokenizeBlockLatex(source) {
  const dollarMatch = /^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*?)\$\$[ \t]*(?:\n|$)/.exec(source);
  if (dollarMatch?.[1] && !containsTableStart(dollarMatch[1])) {
    return { type: "latexBlock", raw: dollarMatch[0], text: dollarMatch[1].trim() };
  }

  const bracketMatch = /^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*?)\\\][ \t]*(?:\n|$)/.exec(source);
  if (bracketMatch?.[1] && !containsTableStart(bracketMatch[1])) {
    return { type: "latexBlock", raw: bracketMatch[0], text: bracketMatch[1].trim() };
  }

  const pendingBracket = /^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*)$/.exec(source);
  if (pendingBracket?.[1] && !containsTableStart(pendingBracket[1])) {
    return { type: "latexBlock", raw: pendingBracket[0], text: pendingBracket[1], pending: true };
  }
  const pendingDollar = /^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*)$/.exec(source);
  if (pendingDollar?.[1] && looksLikePendingDollarMath(pendingDollar[1]) && !containsTableStart(pendingDollar[1])) {
    return { type: "latexBlock", raw: pendingDollar[0], text: pendingDollar[1], pending: true };
  }
  return undefined;
}

const LATEX_MARKDOWN_EXTENSIONS = [
  {
    name: "latexBlock",
    level: "block",
    start(source) {
      const match = /(?:^|\n) {0,3}(?:\$\$|\\\[)/.exec(source);
      return match ? match.index + (match[0].startsWith("\n") ? 1 : 0) : undefined;
    },
    tokenizer: tokenizeBlockLatex,
  },
  {
    name: "latex",
    level: "inline",
    start(source) {
      const indices = [source.indexOf("$"), source.indexOf("\\("), source.indexOf("\\[")].filter(
        (index) => index >= 0,
      );
      return indices.length > 0 ? Math.min(...indices) : undefined;
    },
    tokenizer: tokenizeInlineLatex,
  },
];

// The cases cover pi's additions: strict strikethrough, latex inline and
// block, pending latex, latex next to tables, and CJK emphasis.
const cases = [
  '~~struck~~ text',
  '~~~not struck~~~ text',
  '~~a~~ and ~~b~~',
  '~~nested **bold**~~',
  '$x^2$ inline',
  '$$x^2$$ block',
  '$$\\frac{a}{b}$$',
  '\\[x^2\\] block',
  '\\(x^2\\) inline',
  '$a_1 + b_2$',
  '$100 and $200 dollars',
  'costs 5$ and 10$',
  'CONST $X_CASE$ more',
  'unclosed $x^2 + pending',
  'unclosed \\[x^2 + pending',
  'unclosed $$x^2 + pending\n',
  '$ `code` $',
  'text $$display$$ more',
  '$$\na = b\n$$\n',
  '$$x$$ then\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n',
  '$$\n| a | b |\n| --- | --- |\n| 1 | 2 |\n$$\n',
  'before\n\\[\n\\sum_i i\n\\]\nafter',
  'before \\(a\\) after',
  '**粗体**中文 and ~~删除~~',
  '中文~~删除~~中文',
  '~~中文删除~~中文',
  '- item with $x^2$ inline\n- item two\n',
  '> quote with $x$\n',
  '# heading with $x^2$\n',
  '```\ncode block\n```\n',
  '| math | text |\n| --- | --- |\n| $x^2$ | plain |\n',
  'a $x$ b $y$ c',
  '$$ pending across\n\nnew paragraph',
  '\\[ pending across\n\nnew paragraph',
  '$$\n\\begin{aligned}\na &= b \\\\\nc &= d\n\\end{aligned}\n$$\n',
  'escaped \\$5 not latex',
  '$50-$100 range',
  'x $ y',
  '$$\n| a | b |\n| --- | --- |\n',
];

// canonicalToken mirrors the shape that the Go port writes.
function canonicalToken(token) {
  const out = { type: token.type, raw: token.raw };
  switch (token.type) {
    case "code":
      out.text = token.text;
      out.lang = token.lang ?? "";
      out.codeBlockStyle = token.codeBlockStyle ?? "";
      break;
    case "heading":
      out.text = token.text;
      out.depth = token.depth;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case "blockquote":
      out.text = token.text;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case "list":
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
    case "html":
      out.text = token.text;
      out.pre = token.pre;
      out.block = token.block;
      out.inLink = token.inLink ?? false;
      out.inRawBlock = token.inRawBlock ?? false;
      break;
    case "def":
      out.tag = token.tag;
      out.href = token.href;
      out.title = token.title ?? "";
      break;
    case "table":
      out.align = token.align;
      out.header = token.header.map(canonicalCell);
      out.rows = token.rows.map((row) => row.map(canonicalCell));
      break;
    case "paragraph":
    case "text":
      out.text = token.text;
      out.tokens = (token.tokens ?? []).map(canonicalToken);
      break;
    case "escape":
      out.text = token.text;
      break;
    case "link":
    case "image":
      out.text = token.text;
      out.href = token.href;
      out.title = token.title ?? "";
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case "strong":
    case "em":
    case "del":
      out.text = token.text;
      out.tokens = token.tokens.map(canonicalToken);
      break;
    case "codespan":
      out.text = token.text;
      break;
    case "checkbox":
      out.checked = token.checked;
      break;
    default:
      // the extension tokens of pi, like latex and latexBlock
      out.text = token.text;
      if ("pending" in token) {
        out.pending = token.pending;
      }
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

const markdownParser = new Marked();
markdownParser.setOptions({
  tokenizer: new StrictStrikethroughTokenizer(),
});
markdownParser.use({
  tokenizer: { emStrong: markedCjkFriendly().tokenizer?.emStrong },
  extensions: [...LATEX_MARKDOWN_EXTENSIONS],
});

const out = [];
for (const markdown of cases) {
  const tokens = markdownParser.lexer(markdown).map(canonicalToken);
  out.push({ markdown, tokens });
}

const outFile = join(repoRoot, 'internal/markdown/marked/testdata/pi.json');
writeFileSync(outFile, `${JSON.stringify(out, null, 1)}\n`);
console.log(`wrote ${out.length} cases to ${outFile}`);
