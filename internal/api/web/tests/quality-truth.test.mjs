import assert from "node:assert/strict";
import test from "node:test";
import { canonicalPurity } from "../src/features/nodes/quality/presentation.ts";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { invalidatePurityQueries } from "../src/features/nodes/quality/cache.ts";

test("saving band thresholds refreshes mounted previews and cached detail projections", async () => {
  const client = new QueryClient({defaultOptions: { queries: {retry: false, staleTime: Infinity} }});
  let matched = 1;
  let reads = 0;
  const key = ["platform-scope-preview", "same-criteria"];
  const observer = new QueryObserver(client, {queryKey: key, queryFn: async () => { reads++; return {matched}; }});
  const unsubscribe = observer.subscribe(() => {});
  try {
    await observer.refetch();
    const before = reads;
    for (const prefix of ["node", "intel-node", "platform", "platform-node-facets"]) client.setQueryData([prefix, "sample"], {band: "mixed"});
    matched = 0;
    await invalidatePurityQueries(client);
    assert.equal(client.getQueryData(key).matched, 0);
    assert.ok(reads > before);
    for (const prefix of ["node", "intel-node", "platform", "platform-node-facets"]) assert.equal(client.getQueryState([prefix, "sample"]).isInvalidated, true);
  } finally { unsubscribe(); client.clear(); }
});

test("a provider risk cannot replace the authoritative purity or hot band", () => {
  const summary = { state: "valid", assessment: { purity_score: 60, purity_band: "mixed" }, sources: [{ provider: "ippure", evidence: { risk_score: 10 } }] };
  assert.equal(canonicalPurity(summary).score, 60);
  assert.equal(canonicalPurity(summary).band.id, "mixed");
  summary.assessment.purity_band = "excellent";
  assert.equal(canonicalPurity(summary).score, 60);
  assert.equal(canonicalPurity(summary).band.id, "excellent");
  delete summary.assessment;
  assert.equal(canonicalPurity(summary).score, null);
  assert.equal(canonicalPurity(summary).band, undefined);
});

test("null and invalid scores never inherit a favorable band", () => {
  for (const value of [null, undefined, -1, 101, NaN, 0.5]) {
    assert.deepEqual(canonicalPurity({ assessment: { purity_score: value, purity_band: "excellent" } }), { score: null, band: undefined });
  }
  assert.equal(canonicalPurity({ assessment: { purity_score: 0, purity_band: "poor" } }).score, 0);
});

test("the actual list cell, detail headline and quality badge render the same score and band", async () => {
  const directory = await mkdtemp(join(tmpdir(), "prism-quality-render-"));
  try {
    const outfile = join(directory, "render.cjs");
    await build({
      stdin: { contents: `
        import React from 'react';
        import { renderToStaticMarkup } from 'react-dom/server';
        import { MemoryRouter } from 'react-router-dom';
        import { QualityBadge, QualityDetails } from './src/features/nodes/quality/QualityDetails';
        import { NodeIntelCell } from './src/features/nodes/NodeIntel';
        import { NodeIntelPanel } from './src/features/nodes/NodeIntel';
        import { NodeAttributesPanel } from './src/features/nodes/detail/NodeAttributesPanel';
        import { IPPureReviewPanel } from './src/features/nodes/quality/IPPureReview';
        export { nodeName, nodeTagLabel, nodeTagFromSubscription } from './src/features/nodes/detail/nodeMeta';
        import { NodeEvidenceFacts } from './src/features/nodes/detail/NodeEvidenceFacts';
        import { NodePrivacyFacts } from './src/features/nodes/detail/NodePrivacyFacts';
        import { NodeTransportWarning } from './src/features/nodes/detail/NodeTransportWarning';
        import { NodePrivacySettings } from './src/features/admin/NodePrivacySettings';
        import { PurityGuide } from './src/features/nodes/quality/PurityGuide';
        import { NodeCriteriaFields } from './src/features/platforms/NodeCriteriaFields';
        import { SettingsPage } from './src/features/admin/SettingsPage';
        import { SYSTEM_CONFIG_QUERY_KEY, SYSTEM_CONFIG_DEFAULT_QUERY_KEY, SYSTEM_ENV_CONFIG_QUERY_KEY } from './src/features/systemConfig/api';
        import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
        import { useForm } from 'react-hook-form';
        import { defaultPlatformFormValues } from './src/features/platforms/formModel';
        export function render(summary, intel) {
          return [<QualityBadge summary={summary}/>, <QualityDetails summary={summary}/>, <NodeIntelCell intel={intel}/>]
            .map(element => renderToStaticMarkup(<MemoryRouter>{element}</MemoryRouter>));
        }
        export function gaps(intel) { return renderToStaticMarkup(<NodeEvidenceFacts intel={intel}/>); }
        export function businessFacts(node) {
          const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
          client.setQueryData(['quality','status'], {enabled:true, manual_sources:[]});
          const result = [<NodeAttributesPanel node={node} onCopy={()=>{}}/>, <NodeIntelPanel intel={node.intel} nodeHash={node.node_hash} ready={true} notify={()=>{}}/>, <IPPureReviewPanel nodeHash={node.node_hash} nodeIP={node.egress_ip} ready={true}/>]
            .map(element => renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{element}</MemoryRouter></QueryClientProvider>));
          client.clear();
          return result;
        }
        export function unmanaged() { return renderToStaticMarkup(<NodePrivacyFacts node={{privacy:{status:'unmanaged',reasons:[],warnings:[],identity_check:'unmeasured'}}}/>); }
        export function transport() {
          const node = {privacy:{status:'accepted',reasons:[],warnings:['TLS_VERIFICATION_DISABLED'],identity_check:'unmeasured',findings:{TLS_VERIFICATION_DISABLED:{source_class:'vendor_claim',provider:'ippure'}},identity_evidence:{cause:'MEASUREMENT_DISABLED',action:'匿名性尚未检测',address_family:'IPv4'}}};
          return [<NodeTransportWarning node={node}/>, <NodePrivacyFacts node={node}/>].map(element => renderToStaticMarkup(element));
        }
        export function measurementSwitch() {
          const client = new QueryClient();
          const html = renderToStaticMarkup(<QueryClientProvider client={client}><NodePrivacySettings config={{probe_status:'failed'}}/></QueryClientProvider>);
          client.clear();
          return html;
        }
        export function scoreMeaning() { return renderToStaticMarkup(<PurityGuide/>); }
        function Criteria() { const form = useForm({defaultValues:defaultPlatformFormValues}); return <NodeCriteriaFields form={form} idPrefix='test'/>; }
        export function policies() {
          const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
          const config = {purity_bands:{excellent:60,clean:50,fair:40,mixed:30}};
          client.setQueryData(SYSTEM_CONFIG_QUERY_KEY,config);
          client.setQueryData(SYSTEM_CONFIG_DEFAULT_QUERY_KEY,config);
          client.setQueryData(SYSTEM_ENV_CONFIG_QUERY_KEY,{});
          const result = [<Criteria/>, <SettingsPage/>].map(element => renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{element}</MemoryRouter></QueryClientProvider>));
          client.clear();
          return result;
        }
      `, resolveDir: dirname(dirname(fileURLToPath(import.meta.url))), loader: "tsx" },
      bundle: true, platform: "node", format: "cjs", define: { "import.meta.env": "{}" }, jsx: "automatic", outfile, logLevel: "silent",
    });
    const render = createRequire(import.meta.url)(outfile);
    for (const [cause, message] of [
      ["NO_EGRESS", "请先完成出口探测"],
      ["IPPURE_TIMEOUT", "检测超时，请稍后重试"],
      ["IPPURE_RATE_LIMITED", "检测暂时受限，请稍后重试"],
      ["IPPURE_AUTH_FAILED", "检测暂时不可用，请稍后重试"],
      ["IPPURE_EGRESS_MISMATCH", "出口已变化，请重新检测"],
      ["IPPURE_INVALID_RESPONSE", "检测结果不完整，请重新检测"],
      ["IPPURE_NO_FRAUD_SCORE", "暂未获得有效评分，请重新检测"],
      ["IPPURE_IPV6_SCORE_UNAVAILABLE", "当前 IPv6 出口暂不支持评分"],
      ["EVIDENCE_EXPIRED", "检测结果已过期，请重新检测"],
    ]) {
      const fact = {state:"unknown", source_class:"derived", provider:"ippure", cause, explanation:"来源未返回有效评分", action:"重新运行检测"};
      const assessment = {state:"pending", purity_score:null, purity_band:"unknown", verdict:"pending", network_type:"unknown", reasons:[cause], facts:{purity:fact}};
      for (const html of render.render({state:"pending", assessment, sources:[], evidence:null}, {state:"pending", purity_score:null, facts:{purity:fact}})) {
        const visible = html.replace(/<[^>]*>/g, " ");
        assert.match(visible, /未评分/);
        assert.ok(visible.includes(message), `missing actionable status for ${cause}`);
        assert.doesNotMatch(visible, /来源|provider=|IPPURE_|IPPure|fraudScore|综合证据模型分/);
      }
    }
    const summary = { state: "valid", ip: "203.0.113.1", evidence: null, assessment: { purity_score: 60, purity_band: "excellent", verdict: "incomplete", network_type: "unknown", reasons: [] }, sources: [{ provider: "ippure", state: "valid", evidence: { provider: "ippure", ip: "203.0.113.1", risk_score: 10, fraud_score: 41, valid_until: "2099-01-01T00:00:00Z" } }] };
    for (const html of render.render(summary, { state: "valid", purity_score: 60, purity_band: "excellent", confidence: "low", flags: [] })) {
      assert.match(html, />60</);
      assert.match(html, /极度纯净/);
      assert.doesNotMatch(html, />90</);
      assert.doesNotMatch(html, />41</, "a secondary provider score must not return to the panel");
    }
    const fact = { state: "unknown", source_class: "vendor_claim", provider: "source-fixture", sources: ["source-fixture"], cause: "PROVIDER_UNAVAILABLE", action: "Repair provider access and retry" };
    const gaps = render.gaps({ facts: { proxy: fact, vpn: fact, tor: fact } });
    assert.equal((gaps.match(/检测暂时不可用，请稍后重试/g) ?? []).length, 1);
    assert.match(gaps, /代理.*VPN.*Tor/);
    assert.doesNotMatch(gaps, /来源|供应商|provider|PROVIDER_|source-fixture|Repair provider/);
    const node = {node_hash:"fixture-node", display_tag:"Tokyo Alpha", enabled:true, has_outbound:true, protocol:"http", tags:[{subscription_id:"private-source-id",subscription_name:"private-source-name",tag:"Tokyo Alpha"}], egress_ip:"203.0.113.1", failure_count:0, created_at:"2026-01-01T00:00:00Z", intel:{state:"valid",purity_score:60,purity_band:"excellent",confidence:"high",ip_type:"residential",country:"JP",city:"Tokyo",flags:[],checks:{gemini:"available"},facts:{purity:{state:"known",source_class:"vendor_claim",provider:"ippure",observed_at:"2026-01-01T00:00:00Z",valid_until:"2099-01-01T00:00:00Z"}}}};
    for (const html of render.businessFacts(node)) {
      assert.doesNotMatch(html, /订阅来源|来源与标签|数据来源|技术来源|provider=|IPPure|private-source-(?:name|id)|fraudScore/);
    }
    assert.match(render.businessFacts(node)[0], /Tokyo Alpha/);
    assert.match(render.businessFacts(node)[1], /网页可达（解锁未知）/);
    assert.match(render.businessFacts(node)[1], /检测于.*有效至/);
    const named = {node_hash:"0123456789abcdef",display_tag:"internal/vendor/Tokyo / Relay",tags:[{subscription_id:"fixture",subscription_name:"internal/vendor",tag:"Tokyo / Relay"}]};
    assert.equal(render.nodeName(named), "Tokyo / Relay");
    assert.equal(render.nodeName({node_hash:named.node_hash,display_tag:named.display_tag,subscription_names:["internal/vendor"]}), "Tokyo / Relay");
    assert.equal(render.nodeName({node_hash:named.node_hash,tags:[]}), "0123456789ab");
    assert.equal(render.nodeTagLabel("source/Tokyo / Relay"), "Tokyo / Relay");
    assert.equal(render.nodeTagLabel("Tokyo Alpha"), "Tokyo Alpha");
    assert.equal(render.nodeTagLabel(""), "");
    assert.equal(render.nodeTagFromSubscription("internal/vendor/Tokyo / Relay", "internal/vendor"), "Tokyo / Relay");
    assert.equal(render.nodeTagFromSubscription("Tokyo / Relay", "internal/vendor"), "Tokyo / Relay");
    assert.equal(render.nodeTagFromSubscription("source/source/Relay", "source"), "source/Relay");
    assert.doesNotMatch(render.unmanaged(), /已通过节点准入规则/);
    for (const html of render.transport()) {
      assert.match(html, /上游 TLS 未验证：存在节点冒充与流量读取风险/);
      assert.doesNotMatch(html, /供应商声明|主动网络测量|MEASUREMENT_DISABLED|provider=|IPPure/);
    }
    assert.match(render.measurementSwitch(), /aria-checked="false"/);
    assert.match(render.measurementSwitch(), /已知自有地址仍禁止销售/);
    assert.doesNotMatch(render.measurementSwitch(), /节点销售已暂停/);
    assert.match(render.scoreMeaning(), /0–100/);
    assert.match(render.scoreMeaning(), /分数越高/);
    assert.match(render.scoreMeaning(), /安全概率或网站通过率/);
    assert.doesNotMatch(render.scoreMeaning(), /来源|provider=|IPPure|fraudScore/);
    assert.doesNotMatch(render.scoreMeaning(), /按权重合成|综合证据模型分/);
    for (const html of render.policies()) {
      for (const label of ["极度纯净", "纯净", "较纯净", "一般", "风险较高"]) assert.ok(html.includes(label), `policy surface omitted ${label}`);
      assert.doesNotMatch(html, /极佳|极优|优良|混合阈值/);
    }
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
