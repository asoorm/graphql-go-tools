// Extracts every audit suite into planv2's fixture format:
//   out/<suite>/subgraphs/<name>.graphql
//   out/<suite>/supergraph.graphql        (Apollo-composed CLIENT API schema)
//   out/<suite>/case-NN/operation.graphql
//   out/<suite>/case-NN/expected.json
import { composeServices } from "@apollo/composition";
import { printSchema as printFedSchema } from "@apollo/federation-internals";
import { parse, print } from "graphql";
import * as fs from "node:fs";
import * as path from "node:path";

const suitesDir = "src/test-suites";
const outDir = process.argv[2] ?? "extracted";

const suites = fs.readdirSync(suitesDir).filter((d) =>
  fs.statSync(path.join(suitesDir, d)).isDirectory(),
);

for (const suite of suites) {
  const dir = path.join(suitesDir, suite);
  try {
    const files = fs.readdirSync(dir);
    const subgraphFiles = files.filter((f) => f.endsWith(".subgraph.ts"));
    const subgraphs: Array<{ name: string; typeDefs: string }> = [];
    for (const f of subgraphFiles) {
      const mod = await import(path.resolve(dir, f));
      const sg = mod.default;
      subgraphs.push({ name: sg.name, typeDefs: sg.typeDefs });
    }
    let tests = (await import(path.resolve(dir, "test.ts"))).default;
    if (typeof tests === "function") tests = tests();

    const result = composeServices(
      subgraphs.map((s) => ({
        name: s.name,
        typeDefs: parse(s.typeDefs),
        url: `http://${s.name}`,
      })),
    );
    if (!result.supergraphSdl) {
      console.error(`[${suite}] COMPOSE FAILED:`, result.errors?.map((e) => e.message).join("; "));
      continue;
    }
    // Client API schema (what the gateway serves): federation-internals Schema.toAPISchema.
    const api = printFedSchema((result as any).schema.toAPISchema());

    const suiteOut = path.join(outDir, suite);
    fs.mkdirSync(path.join(suiteOut, "subgraphs"), { recursive: true });
    for (const s of subgraphs) {
      fs.writeFileSync(path.join(suiteOut, "subgraphs", `${s.name}.graphql`), s.typeDefs.trim() + "\n");
    }
    fs.writeFileSync(path.join(suiteOut, "supergraph.graphql"), api.trim() + "\n");
    tests.forEach((t: { query: string; expected: unknown }, i: number) => {
      const caseDir = path.join(suiteOut, `case-${String(i + 1).padStart(2, "0")}`);
      fs.mkdirSync(caseDir, { recursive: true });
      // print(parse(...)) normalizes indentation of the template literal.
      fs.writeFileSync(path.join(caseDir, "operation.graphql"), print(parse(t.query)) + "\n");
      fs.writeFileSync(path.join(caseDir, "expected.json"), JSON.stringify(t.expected, null, 2) + "\n");
    });
    console.log(`[${suite}] ok: ${subgraphs.length} subgraphs, ${tests.length} cases`);
  } catch (err: any) {
    console.error(`[${suite}] ERROR: ${err.message}`);
  }
}
