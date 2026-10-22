'use strict'

async function items() {
  const res = await fetch('https://api.example-unknown.com/v2/items')
  if (!res.ok) throw new Error(`items: ${res.status}`)
  return res.json()
}

async function things() {
  const res = await fetch('https://api.nowhere-documented.com/v1/things')
  return res.json()
}

module.exports = { items, things }
