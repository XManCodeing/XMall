// Exercise the actual frontend serializer, not a second mocked implementation.
// No API client, environment files, services, database, or network are used.
const fs = require('node:fs')
const path = require('node:path')
const root = path.resolve(__dirname, '../../..')
const ts = require(path.join(root, 'frontend/admin/node_modules/typescript'))
const load = (relative, imports) => {
  const source = fs.readFileSync(path.join(root, relative), 'utf8')
  const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', js)((id) => {
    if (!(id in imports)) throw new Error(`Unexpected import: ${id}`)
    return imports[id]
  }, module, module.exports)
  return module.exports
}
const api = load('frontend/admin/src/api/httpIntegration.ts', { './client': { api: {} } })
const ui = load('frontend/admin/src/utils/httpIntegration.ts', { '@/api/httpIntegration': api })
const input = fs.readFileSync(0, 'utf8')
if (input) {
  const { config } = JSON.parse(input)
  config.endpoints = Object.fromEntries(Object.entries(config.endpoints).map(([name, ep]) => [name, ui.serializeEndpoint(ui.endpointDraft(ep))]))
  process.stdout.write(JSON.stringify({ config }))
} else {
  const draft = ui.endpointDraft()
  draft.path = '/ping'
  draft.successPath = 'ok'
  draft.successEquals = 'false'
  draft.fields = [
    { ...ui.emptyField('zero'), value: '0' },
    { ...ui.emptyField('false'), value: 'false' },
    { ...ui.emptyField('empty'), value: '""' },
    { ...ui.emptyField('null'), value: 'null' },
    { ...ui.emptyField('absent') },
    { ...ui.emptyField('defaultZero'), path: 'missing', default: '0' },
    { ...ui.emptyField('defaultFalse'), path: 'missing', default: 'false' },
    { ...ui.emptyField('defaultEmpty'), path: 'missing', default: '""' },
    { ...ui.emptyField('defaultNull'), path: 'missing', default: 'null' },
    { ...ui.emptyField('id'), path: 'id', required: true, transform: 'string' },
  ]
  process.stdout.write(JSON.stringify({ config: { endpoints: { ping: ui.serializeEndpoint(draft) }, credentials: { token: 'fixture-credential' } } }))
}
