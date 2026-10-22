'use strict'

const test = require('node:test')
const assert = require('node:assert')
const { charge, lookup } = require('../src/billing')

function stub() {
  const calls = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url, method: init.method, body: init.body && JSON.parse(init.body) })
    return { ok: true, status: 200, json: async () => ({ id: 'pay_1', amount: 500 }) }
  }
  return calls
}

test('charge sends the amount in cents', async () => {
  const calls = stub()
  assert.deepStrictEqual(await charge(5, 'cus_1'), { id: 'pay_1', amount: 500 })
  assert.strictEqual(calls[0].method, 'POST')
  assert.strictEqual(calls[0].body.amount, 500)
  assert.strictEqual(calls[0].body.customer, 'cus_1')
})

test('lookup asks for one payment', async () => {
  const calls = stub()
  await lookup('pay_1')
  assert.strictEqual(calls[0].method, 'GET')
  assert.match(calls[0].url, /\/pay_1$/)
})

test('lookup needs an id', async () => {
  await assert.rejects(lookup(''), TypeError)
})
