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
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { createI18n } from 'vue-i18n'

const directory = new URL('../src/locales/', import.meta.url)

/**
 * Every `t('some.key')` used in the source must exist in every locale: a typo
 * (or a key added to one namespace by mistake) renders the raw key in the UI,
 * which is exactly the kind of bug a compiling check does not catch.
 */
function usedKeys() {
  const root = new URL('../src/', import.meta.url)
  const files = []
  const walk = (url) => {
    for (const entry of readdirSync(url)) {
      const child = new URL(entry, url)
      if (statSync(child).isDirectory()) walk(new URL(`${entry}/`, url))
      else if (/\.(vue|ts)$/.test(entry)) files.push(child)
    }
  }
  walk(root)

  const keys = new Set()
  for (const file of files) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/\bt(?:c)?\(\s*'([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)'/g)) {
      keys.add(match[1])
    }
  }
  return [...keys].sort()
}

/** hasKey resolves a dotted path inside a messages object. */
function hasKey(messages, key) {
  let node = messages
  for (const part of key.split('.')) {
    if (node === null || typeof node !== 'object' || !(part in node)) return false
    node = node[part]
  }
  return node !== undefined && node !== null
}

/**
 * Every error code of internal/i18n/errors.go must exist in the "errors"
 * namespace of every locale: the API answers with a stable code and the
 * frontend translates it, so a missing key surfaces as a raw "ERR_..." toast.
 */
function errorCodes() {
  const source = readFileSync(new URL('../../internal/i18n/errors.go', import.meta.url), 'utf8')
  return [...source.matchAll(/^\s*Code\w+\s*=\s*"(ERR_[A-Z0-9_]+)"/gm)].map((match) => match[1])
}

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
const codes = errorCodes()
const used = usedKeys()

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

  // Error code parity (see errorCodes above).
  const translated = messages.errors ?? {}
  for (const code of codes) {
    if (!(code in translated)) failures.push(`${locale} errors.${code}: missing translation`)
  }
  for (const key of Object.keys(translated)) {
    if (!codes.includes(key)) failures.push(`${locale} errors.${key}: no matching code in internal/i18n/errors.go`)
  }

  // Every key the code asks for must exist here.
  for (const key of used) {
    if (!hasKey(messages, key)) failures.push(`${locale} ${key}: used in the source but missing here`)
  }

  console.log(`  ${locale.padEnd(6)} ${keys.length} messages`)
}

console.log(`  codes  ${codes.length} error codes checked in every locale`)
console.log(`  usage  ${used.length} keys used by the source checked in every locale`)

if (failures.length > 0) {
  console.error(`x ${failures.length} message(s) do not compile:`)
  failures.forEach((failure) => console.error(`    - ${failure}`))
  process.exit(1)
}
console.log(`ok ${checked} messages compiled`)
