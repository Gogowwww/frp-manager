// Tests de toml.js hors navigateur : node panel/toml.test.mjs
// Tests of toml.js outside the browser: node panel/toml.test.mjs
//
// Couvre les en-têtes et clés entre guillemets, les plages de ports et une
// fusion qui ne touche pas une chaîne multi-lignes. Aucune dépendance.
// Covers quoted headers and keys, port ranges and a merge that leaves a
// multi-line string alone. No dependency.
import assert from 'node:assert/strict';
import {
  parseToml, stripBlocks, tablesAfterBlocks, composeClientConfig,
  proxyFromBlock, visitorFromBlock, generateProxy, mergeConnection,
} from './templates/assets/js/toml.js';

let n = 0;
const check = (name, fn) => {
  try {
    fn();
    n += 1;
    console.log(`ok ${n} - ${name}`);
  } catch (e) {
    process.exitCode = 1;
    console.error(`not ok - ${name}\n${e.message}`);
  }
};

const quoted = [
  'serverAddr = "x"',
  'serverPort = 7000',
  '',
  '[["proxies"]]',
  '"name" = "a"',
  '"type" = "tcp"',
  '"localPort" = 22',
  '"remotePort" = 18_801',
  '',
  '[ "proxies" . \'transport\' ]',
  '"useEncryption" = true',
  '',
  '[["visitors"]] # v',
  "'name' = 'v'",
  '"bindAddr" = "0.0.0.0"',
  '"bindPort" = 18_999',
  '',
  '[ "webServer" ]',
  '"port" = 7_400',
  '',
].join('\n');

check('parseToml : en-têtes et clés entre guillemets', () => {
  const c = parseToml(quoted);
  assert.equal(c.proxies.length, 1);
  assert.equal(c.proxies[0].values.name, 'a');
  assert.equal(c.proxies[0].values.remotePort, '18_801');
  assert.equal(c.proxies[0].values['transport.useEncryption'], 'true');
  assert.equal(c.visitors.length, 1);
  assert.equal(c.visitors[0].values.bindPort, '18_999');
  assert.equal(c.visitors[0].notes['[[]]'].eol, '# v');
  // Clés rangées sous leur clé complète, comme les lit pages/config.js
  // Keys stored under their full key, as pages/config.js reads them
  assert.equal(c.values.serverPort, '7000');
  assert.equal(c.values['webServer.port'], '7_400');
});

check('stripBlocks / tablesAfterBlocks : en-têtes entre guillemets', () => {
  const base = stripBlocks(quoted);
  assert.ok(base.includes('serverAddr = "x"'), base);
  assert.ok(!base.includes('proxies'), base);
  assert.ok(!base.includes('useEncryption'), base);
  const t = tablesAfterBlocks(quoted);
  assert.ok(t.includes('[ "webServer" ]'), t);
  assert.ok(!t.includes('proxies'), t);
});

check('composeClientConfig : blocs entre guillemets réécrits, réglages gardés', () => {
  const c = parseToml(quoted);
  const out = composeClientConfig(quoted, c.proxies.map(proxyFromBlock), c.visitors.map(visitorFromBlock));
  for (const want of ['[[proxies]]', 'name = "a"', 'remotePort = 18801', 'transport.useEncryption = true',
    'serverAddr = "x"', 'bindPort = 18999', '[ "webServer" ]']) {
    assert.ok(out.includes(want), `« ${want} » absent de :\n${out}`);
  }
  const again = parseToml(out);
  assert.equal(again.proxies.length, 1);
  assert.equal(again.proxies[0].values.name, 'a');
  assert.equal(again.visitors.length, 1);
  assert.equal(again.visitors[0].values.bindPort, '18999');
});

const ranged = [
  'serverAddr = "x"',
  '',
  '{{- range $_, $v := parseNumberRangePair "6000-6001" "18850-18851" }}',
  '[["proxies"]]',
  '"name" = "rg-{{ $v.First }}"',
  '"type" = "tcp"',
  '"localPort" = {{ $v.First }}',
  '"remotePort" = {{ $v.Second }}',
  '{{- end }}',
  '',
].join('\n');

check('parseToml : plage de ports dans un bloc entre guillemets', () => {
  const c = parseToml(ranged);
  assert.equal(c.proxies.length, 1);
  assert.deepEqual(c.proxies[0].range, { local: '6000-6001', remote: '18850-18851' });
  const p = proxyFromBlock(c.proxies[0]);
  assert.equal(p.name, 'rg');
  assert.equal(p.localPort, '6000-6001');
  assert.equal(p.remotePort, '18850-18851');
});

check('generateProxy : commentaire de fin de l\'en-tête entre guillemets gardé', () => {
  const c = parseToml('[["proxies"]] # note\n"name" = "a"\n"type" = "tcp"\n"localPort" = 22\n"remotePort" = 6000\n');
  const out = generateProxy(proxyFromBlock(c.proxies[0]));
  assert.ok(out.startsWith('[[proxies]] # note'), out);
});

const src1 = ['metadatas.m = """', '# not a comment', '[[proxies]]', '"""', '', '# journal', '[log]', 'level = "info"', ''].join('\n');

check('mergeConnection : chaîne multi-lignes intacte sous une clé racine ajoutée', () => {
  const out = mergeConnection(src1, 'bindPort = 9\n', new Set(['bindPort']));
  assert.ok(out.includes('metadatas.m = """\n# not a comment\n[[proxies]]\n"""'), out);
  const c = parseToml(out);
  assert.equal(c.values.bindPort, '9');
  assert.equal(c.values['log.level'], 'info');
  // Le saut de ligne qui précède « """ » fait partie de la valeur, celui qui
  // suit l'ouverture non (règle TOML)
  // The line break before « """ » is part of the value, the one after the
  // opening is not (TOML rule)
  assert.equal(c.values['metadatas.m'], '# not a comment\n[[proxies]]\n');
});

const src2 = ['notes = """', 'text', '# inside', '"""', '{{- if .Envs.X }}', '[[proxies]]', 'name = "a"', '{{- end }}', ''].join('\n');

check('mergeConnection : nouvelle table après une chaîne, hors bloc de modèle', () => {
  const out = mergeConnection(src2, '[auth]\ntoken = "t"\n', new Set(['auth.token']));
  assert.ok(out.includes('"""\n\n[auth]\ntoken = "t"\n'), out);
  const auth = out.indexOf('[auth]');
  assert.ok(auth > out.lastIndexOf('"""'), out);
  assert.ok(auth < out.indexOf('{{- if'), out);
  const c = parseToml(out);
  assert.equal(c.values['auth.token'], 't');
  assert.equal(c.proxies.length, 1);
  assert.equal(c.proxies[0].values.name, 'a');
});

if (process.exitCode) {
  console.error(`\n${n} vérifications passées, des échecs ci-dessus / ${n} checks passed, failures above`);
} else {
  console.log(`\n${n} vérifications passées / checks passed`);
}
