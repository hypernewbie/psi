// Regenerate embedded metadata from the installed public Pi AI catalog.
// Never point this tool at a Pi agent directory or a model cache.
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { createHash } from "node:crypto";

const [packageArg, outputArg] = process.argv.slice(2);
if (!packageArg || !outputArg) {
	throw new Error("usage: node scripts/snapshot-models.mjs PI_AI_PACKAGE OUTPUT_DIRECTORY");
}

const root = resolve(packageArg);
const out = resolve(outputArg);
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
if (pkg.name !== "@earendil-works/pi-ai") {
	throw new Error("input must be the public pi-ai package");
}

const providers = ["anthropic", "openai-codex", "minimax", "opencode-go", "deepseek", "zai", "google"];
const catalog = {};
const hashes = {};
const manifest = JSON.parse(readFileSync(join(root, "dist/providers/data/.manifest.json"), "utf8"));
if (!manifest.generatedAt || Number.isNaN(Date.parse(manifest.generatedAt))) {
	throw new Error("public catalog manifest lacks a valid generation date");
}

function publicChatModel(model) {
	if (!model || model.type && model.type !== "chat" || typeof model.id !== "string" || typeof model.api !== "string") {
		return undefined;
	}
	const result = {
		id: model.id,
		api: model.api,
		baseUrl: model.baseUrl,
		name: model.name,
		contextWindow: model.contextWindow,
		maxTokens: model.maxTokens,
		reasoning: model.reasoning,
		input: model.input,
	};
	for (const key of ["thinkingLevelMap", "compat"]) {
		if (Object.hasOwn(model, key)) result[key] = model[key];
	}
	return result;
}

for (const provider of providers) {
	const path = join(root, "dist/providers/data", provider + ".json");
	const data = readFileSync(path);
	hashes[provider] = createHash("sha256").update(data).digest("hex");
	const source = JSON.parse(data);
	const models = Object.values(source)
		.flatMap((api) => Object.values(api))
		.filter((model) => model.type === "chat")
		.map(publicChatModel)
		.filter(Boolean)
		.sort((a, b) => a.id.localeCompare(b.id));
	if (models.some((model) => model.api === "" || !model.baseUrl)) {
		throw new Error(`${provider} public chat catalog contains an incomplete API or endpoint`);
	}
	catalog[provider] = models;
}

mkdirSync(out, { recursive: true });
writeFileSync(join(out, "catalog.json"), JSON.stringify(catalog, null, 2) + "\n");
writeFileSync(
	join(out, "catalog-source.json"),
	JSON.stringify(
		{
			package: pkg.name,
			version: pkg.version,
			generatedAt: manifest.generatedAt,
			sha256: hashes,
			protocolReference: "d5b3de22d0e24a3e9c38a9410d3329b099717283",
			generator: "snapshot-models-v2",
			providers,
			scope: "chat model IDs, APIs, public endpoints, input/reasoning metadata, thinking maps, and compatibility metadata only; no credentials or headers",
		},
		null,
		2,
	) + "\n",
);
console.log("Public catalog snapshot generated: " + providers.map((provider) => `${provider}=${catalog[provider].length}`).join(", "));
