const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const template = fs.readFileSync(path.join(__dirname, '../admin_templates/settings.html'), 'utf8');
const source = template.slice(template.indexOf('    function parseWeightedTerm('), template.indexOf('    function handleWeightedTagInput('));
const preset = process.env.RD_PRESET_TERM;
assert.ok(preset, 'Run through go test to supply the settings schema preset');
assert.equal(preset, String.raw`/(?-i)(?:WEB-DL|WEB\.x264|WEB\.H264|HDTV\.x264|HDTV\.XviD)/`);
const legacy = String.raw`/(?i)(?:web-dl|webrip|bdrip|hdrip|dvdrip|bluray\.x264|hdtv\.(?:x264|xvid)|web\.(?:x264|h264))/`;

function setup(initial) {
  let value = initial;
  const writes = [];
  const context = vm.createContext({
    schema: { filtering: { fields: { nonPreferredTerms: { quickAddTerm: preset } } } },
    getValue: () => value,
    setValue: (key, next) => { writes.push(key); value = next; },
    renderSettings() {}, showToast() {},
  });
  vm.runInContext(source, context);
  return { context, writes, values: () => Array.from(value) };
}

test('quick add uses the current preset at global, service, profile and device paths', () => {
  for (const basePath of ['filtering', 'filtering.debrid', 'filtering.usenet', '']) {
    const { context, writes, values } = setup(['REMUX=3']);
    context.addRestrictedFileNonPreferredTerms(basePath, 'nonPreferredTerms');
    assert.deepEqual(values(), ['REMUX=3', preset + '=10']);
    assert.deepEqual(writes, [basePath ? basePath + '.nonPreferredTerms' : 'nonPreferredTerms']);
  }
});

test('quick add replaces the legacy preset and removes duplicate presets', () => {
  const { context, values } = setup(['AV1=2', legacy + '=4', preset + '=7', legacy + '=10', 'REMUX=3']);
  context.addRestrictedFileNonPreferredTerms('filtering', 'nonPreferredTerms');
  assert.deepEqual(values(), ['AV1=2', preset + '=10', 'REMUX=3']);
  context.addRestrictedFileNonPreferredTerms('filtering', 'nonPreferredTerms');
  assert.deepEqual(values(), ['AV1=2', preset + '=10', 'REMUX=3']);
});

test('case-sensitive custom regexes are preserved', () => {
  const custom = preset.replace('WEB.H264', 'web.H264').replace('WEB\\.H264', 'web\\.H264');
  const { context, values } = setup([custom + '=5']);
  context.addRestrictedFileNonPreferredTerms('filtering', 'nonPreferredTerms');
  assert.deepEqual(values(), [custom + '=5', preset + '=10']);
});

test('missing or malformed term arrays can receive the preset', () => {
  for (const initial of [undefined, null, 'bad-value', {}]) {
    const { context, values } = setup(initial);
    context.addRestrictedFileNonPreferredTerms('filtering', 'nonPreferredTerms');
    assert.deepEqual(values(), [preset + '=10']);
  }
});
