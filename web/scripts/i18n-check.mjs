#!/usr/bin/env node
/**
 * Compiles every message of every locale with vue-i18n.
 *
 * vue-i18n compiles a message the first time it is rendered and throws on a
 * syntax error. A `{{.Event}}` placeholder in `notifications.webhookBodyHelp`
 * reached the bundle and took the whole page down ("Message compilation error:
 * Not allowed nest placeholder") the moment the webhook dialog rendered it - and
 * only then, which is why the smoke test never saw it. Compiling all of them
 * here turns that class of blank page into a failing check.
 *
 * Usage: npm run check:i18n
 */
import { readdirSync, readFileSync } from 'node:fs'
import { createI18n } from 'vue-i18n'

const directory = new URL('../src/locales/', import.meta.url)

/** keysOf flattens the nested message tree into dot separated keys. */
function keysOf(messages, prefix = '') {
  const keys = []
  for (const [key, value] of Object.entries(messages)) {
    if (value && typeof value === 'object') keys.push(...keysOf(value, `${prefix}${key}.`))
    else keys.push(`${prefix}${key}`)
  }
  return keys
}

let checked = 0
const failures = []

for (const file of readdirSync(directory).filter((name) => name.endsWith('.json')).sort()) {
  const locale = file.replace(/\.json$/, '')
  const messages = JSON.parse(readFileSync(new URL(file, directory), 'utf8'))
  const i18n = createI18n({
    legacy: false,
    locale,
    messages: { [locale]: messages },
    missingWarn: false,
    fallbackWarn: false,
  })
  const keys = keysOf(messages)
  for (const key of keys) {
    try {
      // Rendering is what compiles the message: use a locale that exists so the
      // fallback chain does not hide a failure behind another message.
      i18n.global.t(key)
      checked += 1
    } catch (error) {
      failures.push(`${locale} ${key}: ${String(error.message).split('\n')[0]}`)
    }
  }
  console.log(`  ${locale.padEnd(6)} ${keys.length} messages`)
}

if (failures.length > 0) {
  console.error(`x ${failures.length} message(s) do not compile:`)
  failures.forEach((failure) => console.error(`    - ${failure}`))
  process.exit(1)
}
console.log(`ok ${checked} messages compiled`)
