// Icon generator: rasterizes web/public/logo.svg into the PNG sizes used by the
// favicon, the PWA manifest and the apple-touch-icon.
//
// resvg-js is intentionally NOT a dependency of the web package (it would only
// slow down the image build), so install it in a throwaway prefix:
//
//   npm install --prefix /tmp/up-icons @resvg/resvg-js
//   NODE_PATH=/tmp/up-icons/node_modules node tools/generate-icons.mjs /path/to/up
//
// The favicon.ico is assembled afterwards with Pillow (16/32/48), see
// docs/development.md.
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { Resvg } from '@resvg/resvg-js'

const root = process.argv[2] ?? resolve(import.meta.dirname, '..')
const svg = readFileSync(resolve(root, 'web/public/logo.svg'), 'utf8')

const targets = [
  ['favicon-16.png', 16],
  ['favicon-32.png', 32],
  ['favicon-48.png', 48],
  ['logo-64.png', 64],
  ['apple-touch-icon.png', 180],
  ['logo-192.png', 192],
  ['logo-512.png', 512],
]

for (const [name, size] of targets) {
  const resvg = new Resvg(svg, { fitTo: { mode: 'width', value: size } })
  const png = resvg.render().asPng()
  writeFileSync(resolve(root, 'web/public', name), png)
  console.log(`${name}: ${png.length} bytes`)
}
